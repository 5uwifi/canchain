//
// (at your option) any later version.
//
//

package bloombits

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/5uwifi/canchain/common/bitutil"
	"github.com/5uwifi/canchain/basis/crypto"
)

type bloomIndexes [3]uint

func calcBloomIndexes(b []byte) bloomIndexes {
	b = crypto.Keccak256(b)

	var idxs bloomIndexes
	for i := 0; i < len(idxs); i++ {
		idxs[i] = (uint(b[2*i])<<8)&2047 + uint(b[2*i+1])
	}
	return idxs
}

type partialMatches struct {
	section uint64
	bitset  []byte
}

//
type Retrieval struct {
	Bit      uint
	Sections []uint64
	Bitsets  [][]byte

	Context context.Context
	Error   error
}

type Matcher struct {
	sectionSize uint64 // Size of the data batches to filter on

	filters    [][]bloomIndexes    // Filter the system is matching for
	schedulers map[uint]*scheduler // Retrieval schedulers for loading bloom bits

	retrievers chan chan uint       // Retriever processes waiting for bit allocations
	counters   chan chan uint       // Retriever processes waiting for task count reports
	retrievals chan chan *Retrieval // Retriever processes waiting for task allocations
	deliveries chan *Retrieval      // Retriever processes waiting for task response deliveries

	running uint32 // Atomic flag whether a session is live or not
}

func NewMatcher(sectionSize uint64, filters [][][]byte) *Matcher {
	// Create the matcher instance
	m := &Matcher{
		sectionSize: sectionSize,
		schedulers:  make(map[uint]*scheduler),
		retrievers:  make(chan chan uint),
		counters:    make(chan chan uint),
		retrievals:  make(chan chan *Retrieval),
		deliveries:  make(chan *Retrieval),
	}
	// Calculate the bloom bit indexes for the groups we're interested in
	m.filters = nil

	for _, filter := range filters {
		// Gather the bit indexes of the filter rule, special casing the nil filter
		if len(filter) == 0 {
			continue
		}
		bloomBits := make([]bloomIndexes, len(filter))
		for i, clause := range filter {
			if clause == nil {
				bloomBits = nil
				break
			}
			bloomBits[i] = calcBloomIndexes(clause)
		}
		// Accumulate the filter rules if no nil rule was within
		if bloomBits != nil {
			m.filters = append(m.filters, bloomBits)
		}
	}
	// For every bit, create a scheduler to load/download the bit vectors
	for _, bloomIndexLists := range m.filters {
		for _, bloomIndexList := range bloomIndexLists {
			for _, bloomIndex := range bloomIndexList {
				m.addScheduler(bloomIndex)
			}
		}
	}
	return m
}

func (m *Matcher) addScheduler(idx uint) {
	if _, ok := m.schedulers[idx]; ok {
		return
	}
	m.schedulers[idx] = newScheduler(idx)
}

func (m *Matcher) Start(ctx context.Context, begin, end uint64, results chan uint64) (*MatcherSession, error) {
	// Make sure we're not creating concurrent sessions
	if atomic.SwapUint32(&m.running, 1) == 1 {
		return nil, errors.New("matcher already running")
	}
	defer atomic.StoreUint32(&m.running, 0)

	// Initiate a new matching round
	session := &MatcherSession{
		matcher: m,
		quit:    make(chan struct{}),
		kill:    make(chan struct{}),
		ctx:     ctx,
	}
	for _, scheduler := range m.schedulers {
		scheduler.reset()
	}
	sink := m.run(begin, end, cap(results), session)

	// Read the output from the result sink and deliver to the user
	session.pend.Add(1)
	go func() {
		defer session.pend.Done()
		defer close(results)

		for {
			select {
			case <-session.quit:
				return

			case res, ok := <-sink:
				// New match result found
				if !ok {
					return
				}
				// Calculate the first and last blocks of the section
				sectionStart := res.section * m.sectionSize

				first := sectionStart
				if begin > first {
					first = begin
				}
				last := sectionStart + m.sectionSize - 1
				if end < last {
					last = end
				}
				// Iterate over all the blocks in the section and return the matching ones
				for i := first; i <= last; i++ {
					// Skip the entire byte if no matches are found inside (and we're processing an entire byte!)
					next := res.bitset[(i-sectionStart)/8]
					if next == 0 {
						if i%8 == 0 {
							i += 7
						}
						continue
					}
					// Some bit it set, do the actual submatching
					if bit := 7 - i%8; next&(1<<bit) != 0 {
						select {
						case <-session.quit:
							return
						case results <- i:
						}
					}
				}
			}
		}
	}()
	return session, nil
}

//
func (m *Matcher) run(begin, end uint64, buffer int, session *MatcherSession) chan *partialMatches {
	// Create the source channel and feed section indexes into
	source := make(chan *partialMatches, buffer)

	session.pend.Add(1)
	go func() {
		defer session.pend.Done()
		defer close(source)

		for i := begin / m.sectionSize; i <= end/m.sectionSize; i++ {
			select {
			case <-session.quit:
				return
			case source <- &partialMatches{i, bytes.Repeat([]byte{0xff}, int(m.sectionSize/8))}:
			}
		}
	}()
	// Assemble the daisy-chained filtering pipeline
	next := source
	dist := make(chan *request, buffer)

	for _, bloom := range m.filters {
		next = m.subMatch(next, dist, bloom, session)
	}
	// Start the request distribution
	session.pend.Add(1)
	go m.distributor(dist, session)

	return next
}

func (m *Matcher) subMatch(source chan *partialMatches, dist chan *request, bloom []bloomIndexes, session *MatcherSession) chan *partialMatches {
	// Start the concurrent schedulers for each bit required by the bloom filter
	sectionSources := make([][3]chan uint64, len(bloom))
	sectionSinks := make([][3]chan []byte, len(bloom))
	for i, bits := range bloom {
		for j, bit := range bits {
			sectionSources[i][j] = make(chan uint64, cap(source))
			sectionSinks[i][j] = make(chan []byte, cap(source))

			m.schedulers[bit].run(sectionSources[i][j], dist, sectionSinks[i][j], session.quit, &session.pend)
		}
	}

	process := make(chan *partialMatches, cap(source)) // entries from source are forwarded here after fetches have been initiated
	results := make(chan *partialMatches, cap(source))

	session.pend.Add(2)
	go func() {
		// Tear down the goroutine and terminate all source channels
		defer session.pend.Done()
		defer close(process)

		defer func() {
			for _, bloomSources := range sectionSources {
				for _, bitSource := range bloomSources {
					close(bitSource)
				}
			}
		}()
		// Read sections from the source channel and multiplex into all bit-schedulers
		for {
			select {
			case <-session.quit:
				return

			case subres, ok := <-source:
				// New subresult from previous link
				if !ok {
					return
				}
				// Multiplex the section index to all bit-schedulers
				for _, bloomSources := range sectionSources {
					for _, bitSource := range bloomSources {
						select {
						case <-session.quit:
							return
						case bitSource <- subres.section:
						}
					}
				}
				// Notify the processor that this section will become available
				select {
				case <-session.quit:
					return
				case process <- subres:
				}
			}
		}
	}()

	go func() {
		// Tear down the goroutine and terminate the final sink channel
		defer session.pend.Done()
		defer close(results)

		// Read the source notifications and collect the delivered results
		for {
			select {
			case <-session.quit:
				return

			case subres, ok := <-process:
				// Notified of a section being retrieved
				if !ok {
					return
				}
				// Gather all the sub-results and merge them together
				var orVector []byte
				for _, bloomSinks := range sectionSinks {
					var andVector []byte
					for _, bitSink := range bloomSinks {
						var data []byte
						select {
						case <-session.quit:
							return
						case data = <-bitSink:
						}
						if andVector == nil {
							andVector = make([]byte, int(m.sectionSize/8))
							copy(andVector, data)
						} else {
							bitutil.ANDBytes(andVector, andVector, data)
						}
					}
					if orVector == nil {
						orVector = andVector
					} else {
						bitutil.ORBytes(orVector, orVector, andVector)
					}
				}

				if orVector == nil {
					orVector = make([]byte, int(m.sectionSize/8))
				}
				if subres.bitset != nil {
					bitutil.ANDBytes(orVector, orVector, subres.bitset)
				}
				if bitutil.TestBytes(orVector) {
					select {
					case <-session.quit:
						return
					case results <- &partialMatches{subres.section, orVector}:
					}
				}
			}
		}
	}()
	return results
}

func (m *Matcher) distributor(dist chan *request, session *MatcherSession) {
	defer session.pend.Done()

	var (
		requests   = make(map[uint][]uint64) // Per-bit list of section requests, ordered by section number
		unallocs   = make(map[uint]struct{}) // Bits with pending requests but not allocated to any retriever
		retrievers chan chan uint            // Waiting retrievers (toggled to nil if unallocs is empty)
	)
	var (
		allocs   int            // Number of active allocations to handle graceful shutdown requests
		shutdown = session.quit // Shutdown request channel, will gracefully wait for pending requests
	)

	// assign is a helper method fo try to assign a pending bit an actively
	// listening servicer, or schedule it up for later when one arrives.
	assign := func(bit uint) {
		select {
		case fetcher := <-m.retrievers:
			allocs++
			fetcher <- bit
		default:
			// No retrievers active, start listening for new ones
			retrievers = m.retrievers
			unallocs[bit] = struct{}{}
		}
	}

	for {
		select {
		case <-shutdown:
			// Graceful shutdown requested, wait until all pending requests are honoured
			if allocs == 0 {
				return
			}
			shutdown = nil

		case <-session.kill:
			// Pending requests not honoured in time, hard terminate
			return

		case req := <-dist:
			// New retrieval request arrived to be distributed to some fetcher process
			queue := requests[req.bit]
			index := sort.Search(len(queue), func(i int) bool { return queue[i] >= req.section })
			requests[req.bit] = append(queue[:index], append([]uint64{req.section}, queue[index:]...)...)

			// If it's a new bit and we have waiting fetchers, allocate to them
			if len(queue) == 0 {
				assign(req.bit)
			}

		case fetcher := <-retrievers:
			// New retriever arrived, find the lowest section-ed bit to assign
			bit, best := uint(0), uint64(math.MaxUint64)
			for idx := range unallocs {
				if requests[idx][0] < best {
					bit, best = idx, requests[idx][0]
				}
			}
			// Stop tracking this bit (and alloc notifications if no more work is available)
			delete(unallocs, bit)
			if len(unallocs) == 0 {
				retrievers = nil
			}
			allocs++
			fetcher <- bit

		case fetcher := <-m.counters:
			// New task count request arrives, return number of items
			fetcher <- uint(len(requests[<-fetcher]))

		case fetcher := <-m.retrievals:
			// New fetcher waiting for tasks to retrieve, assign
			task := <-fetcher
			if want := len(task.Sections); want >= len(requests[task.Bit]) {
				task.Sections = requests[task.Bit]
				delete(requests, task.Bit)
			} else {
				task.Sections = append(task.Sections[:0], requests[task.Bit][:want]...)
				requests[task.Bit] = append(requests[task.Bit][:0], requests[task.Bit][want:]...)
			}
			fetcher <- task

			// If anything was left unallocated, try to assign to someone else
			if len(requests[task.Bit]) > 0 {
				assign(task.Bit)
			}

		case result := <-m.deliveries:
			// New retrieval task response from fetcher, split out missing sections and
			// deliver complete ones
			var (
				sections = make([]uint64, 0, len(result.Sections))
				bitsets  = make([][]byte, 0, len(result.Bitsets))
				missing  = make([]uint64, 0, len(result.Sections))
			)
			for i, bitset := range result.Bitsets {
				if len(bitset) == 0 {
					missing = append(missing, result.Sections[i])
					continue
				}
				sections = append(sections, result.Sections[i])
				bitsets = append(bitsets, bitset)
			}
			m.schedulers[result.Bit].deliver(sections, bitsets)
			allocs--

			// Reschedule missing sections and allocate bit if newly available
			if len(missing) > 0 {
				queue := requests[result.Bit]
				for _, section := range missing {
					index := sort.Search(len(queue), func(i int) bool { return queue[i] >= section })
					queue = append(queue[:index], append([]uint64{section}, queue[index:]...)...)
				}
				requests[result.Bit] = queue

				if len(queue) == len(missing) {
					assign(result.Bit)
				}
			}
			// If we're in the process of shutting down, terminate
			if allocs == 0 && shutdown == nil {
				return
			}
		}
	}
}

type MatcherSession struct {
	matcher *Matcher

	closer sync.Once     // Sync object to ensure we only ever close once
	quit   chan struct{} // Quit channel to request pipeline termination
	kill   chan struct{} // Term channel to signal non-graceful forced shutdown

	ctx context.Context // Context used by the light client to abort filtering
	err atomic.Value    // Global error to track retrieval failures deep in the chain

	pend sync.WaitGroup
}

func (s *MatcherSession) Close() {
	s.closer.Do(func() {
		// Signal termination and wait for all goroutines to tear down
		close(s.quit)
		time.AfterFunc(time.Second, func() { close(s.kill) })
		s.pend.Wait()
	})
}

func (s *MatcherSession) Error() error {
	if err := s.err.Load(); err != nil {
		return err.(error)
	}
	return nil
}

func (s *MatcherSession) AllocateRetrieval() (uint, bool) {
	fetcher := make(chan uint)

	select {
	case <-s.quit:
		return 0, false
	case s.matcher.retrievers <- fetcher:
		bit, ok := <-fetcher
		return bit, ok
	}
}

func (s *MatcherSession) PendingSections(bit uint) int {
	fetcher := make(chan uint)

	select {
	case <-s.quit:
		return 0
	case s.matcher.counters <- fetcher:
		fetcher <- bit
		return int(<-fetcher)
	}
}

func (s *MatcherSession) AllocateSections(bit uint, count int) []uint64 {
	fetcher := make(chan *Retrieval)

	select {
	case <-s.quit:
		return nil
	case s.matcher.retrievals <- fetcher:
		task := &Retrieval{
			Bit:      bit,
			Sections: make([]uint64, count),
		}
		fetcher <- task
		return (<-fetcher).Sections
	}
}

func (s *MatcherSession) DeliverSections(bit uint, sections []uint64, bitsets [][]byte) {
	select {
	case <-s.kill:
		return
	case s.matcher.deliveries <- &Retrieval{Bit: bit, Sections: sections, Bitsets: bitsets}:
	}
}

//
func (s *MatcherSession) Multiplex(batch int, wait time.Duration, mux chan chan *Retrieval) {
	for {
		// Allocate a new bloom bit index to retrieve data for, stopping when done
		bit, ok := s.AllocateRetrieval()
		if !ok {
			return
		}
		// Bit allocated, throttle a bit if we're below our batch limit
		if s.PendingSections(bit) < batch {
			select {
			case <-s.quit:
				// Session terminating, we can't meaningfully service, abort
				s.AllocateSections(bit, 0)
				s.DeliverSections(bit, []uint64{}, [][]byte{})
				return

			case <-time.After(wait):
				// Throttling up, fetch whatever's available
			}
		}
		// Allocate as much as we can handle and request servicing
		sections := s.AllocateSections(bit, batch)
		request := make(chan *Retrieval)

		select {
		case <-s.quit:
			// Session terminating, we can't meaningfully service, abort
			s.DeliverSections(bit, sections, make([][]byte, len(sections)))
			return

		case mux <- request:
			// Retrieval accepted, something must arrive before we're aborting
			request <- &Retrieval{Bit: bit, Sections: sections, Context: s.ctx}

			result := <-request
			if result.Error != nil {
				s.err.Store(result.Error)
				s.Close()
			}
			s.DeliverSections(result.Bit, result.Sections, result.Bitsets)
		}
	}
}
