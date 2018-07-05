//
// (at your option) any later version.
//
//

package canhash

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"time"
	"unsafe"

	mmap "github.com/edsrzf/mmap-go"
	"github.com/5uwifi/canchain/consensus"
	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/5uwifi/canchain/basis/metrics"
	"github.com/5uwifi/canchain/rpc"
	"github.com/hashicorp/golang-lru/simplelru"
)

var ErrInvalidDumpMagic = errors.New("invalid dump magic")

var (
	// maxUint256 is a big integer representing 2^256-1
	maxUint256 = new(big.Int).Exp(big.NewInt(2), big.NewInt(256), big.NewInt(0))

	// sharedEthash is a full instance that can be shared between multiple users.
	sharedCanhash = New(Config{"", 3, 0, "", 1, 0, ModeNormal})

	// algorithmRevision is the data structure version used for file naming.
	algorithmRevision = 23

	// dumpMagic is a dataset dump header to sanity check a data dump.
	dumpMagic = []uint32{0xbaddcafe, 0xfee1dead}
)

func isLittleEndian() bool {
	n := uint32(0x01020304)
	return *(*byte)(unsafe.Pointer(&n)) == 0x04
}

func memoryMap(path string) (*os.File, mmap.MMap, []uint32, error) {
	file, err := os.OpenFile(path, os.O_RDONLY, 0644)
	if err != nil {
		return nil, nil, nil, err
	}
	mem, buffer, err := memoryMapFile(file, false)
	if err != nil {
		file.Close()
		return nil, nil, nil, err
	}
	for i, magic := range dumpMagic {
		if buffer[i] != magic {
			mem.Unmap()
			file.Close()
			return nil, nil, nil, ErrInvalidDumpMagic
		}
	}
	return file, mem, buffer[len(dumpMagic):], err
}

func memoryMapFile(file *os.File, write bool) (mmap.MMap, []uint32, error) {
	// Try to memory map the file
	flag := mmap.RDONLY
	if write {
		flag = mmap.RDWR
	}
	mem, err := mmap.Map(file, flag, 0)
	if err != nil {
		return nil, nil, err
	}
	// Yay, we managed to memory map the file, here be dragons
	header := *(*reflect.SliceHeader)(unsafe.Pointer(&mem))
	header.Len /= 4
	header.Cap /= 4

	return mem, *(*[]uint32)(unsafe.Pointer(&header)), nil
}

func memoryMapAndGenerate(path string, size uint64, generator func(buffer []uint32)) (*os.File, mmap.MMap, []uint32, error) {
	// Ensure the data folder exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, nil, nil, err
	}
	// Create a huge temporary empty file to fill with data
	temp := path + "." + strconv.Itoa(rand.Int())

	dump, err := os.Create(temp)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = dump.Truncate(int64(len(dumpMagic))*4 + int64(size)); err != nil {
		return nil, nil, nil, err
	}
	// Memory map the file for writing and fill it with the generator
	mem, buffer, err := memoryMapFile(dump, true)
	if err != nil {
		dump.Close()
		return nil, nil, nil, err
	}
	copy(buffer, dumpMagic)

	data := buffer[len(dumpMagic):]
	generator(data)

	if err := mem.Unmap(); err != nil {
		return nil, nil, nil, err
	}
	if err := dump.Close(); err != nil {
		return nil, nil, nil, err
	}
	if err := os.Rename(temp, path); err != nil {
		return nil, nil, nil, err
	}
	return memoryMap(path)
}

type lru struct {
	what string
	new  func(epoch uint64) interface{}
	mu   sync.Mutex
	// Items are kept in a LRU cache, but there is a special case:
	// We always keep an item for (highest seen epoch) + 1 as the 'future item'.
	cache      *simplelru.LRU
	future     uint64
	futureItem interface{}
}

func newlru(what string, maxItems int, new func(epoch uint64) interface{}) *lru {
	if maxItems <= 0 {
		maxItems = 1
	}
	cache, _ := simplelru.NewLRU(maxItems, func(key, value interface{}) {
		log4j.Trace("Evicted ethash "+what, "epoch", key)
	})
	return &lru{what: what, new: new, cache: cache}
}

func (lru *lru) get(epoch uint64) (item, future interface{}) {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	// Get or create the item for the requested epoch.
	item, ok := lru.cache.Get(epoch)
	if !ok {
		if lru.future > 0 && lru.future == epoch {
			item = lru.futureItem
		} else {
			log4j.Trace("Requiring new ethash "+lru.what, "epoch", epoch)
			item = lru.new(epoch)
		}
		lru.cache.Add(epoch, item)
	}
	// Update the 'future item' if epoch is larger than previously seen.
	if epoch < maxEpoch-1 && lru.future < epoch+1 {
		log4j.Trace("Requiring new future ethash "+lru.what, "epoch", epoch+1)
		future = lru.new(epoch + 1)
		lru.future = epoch + 1
		lru.futureItem = future
	}
	return item, future
}

type cache struct {
	epoch uint64    // Epoch for which this cache is relevant
	dump  *os.File  // File descriptor of the memory mapped cache
	mmap  mmap.MMap // Memory map itself to unmap before releasing
	cache []uint32  // The actual cache data content (may be memory mapped)
	once  sync.Once // Ensures the cache is generated only once
}

func newCache(epoch uint64) interface{} {
	return &cache{epoch: epoch}
}

func (c *cache) generate(dir string, limit int, test bool) {
	c.once.Do(func() {
		size := cacheSize(c.epoch*epochLength + 1)
		seed := seedHash(c.epoch*epochLength + 1)
		if test {
			size = 1024
		}
		// If we don't store anything on disk, generate and return.
		if dir == "" {
			c.cache = make([]uint32, size/4)
			generateCache(c.cache, c.epoch, seed)
			return
		}
		// Disk storage is needed, this will get fancy
		var endian string
		if !isLittleEndian() {
			endian = ".be"
		}
		path := filepath.Join(dir, fmt.Sprintf("cache-R%d-%x%s", algorithmRevision, seed[:8], endian))
		logger := log4j.New("epoch", c.epoch)

		// We're about to mmap the file, ensure that the mapping is cleaned up when the
		// cache becomes unused.
		runtime.SetFinalizer(c, (*cache).finalizer)

		// Try to load the file from disk and memory map it
		var err error
		c.dump, c.mmap, c.cache, err = memoryMap(path)
		if err == nil {
			logger.Debug("Loaded old canhash cache from disk")
			return
		}
		logger.Debug("Failed to load old canhash cache", "err", err)

		// No previous cache available, create a new cache file to fill
		c.dump, c.mmap, c.cache, err = memoryMapAndGenerate(path, size, func(buffer []uint32) { generateCache(buffer, c.epoch, seed) })
		if err != nil {
			logger.Error("Failed to generate mapped canhash cache", "err", err)

			c.cache = make([]uint32, size/4)
			generateCache(c.cache, c.epoch, seed)
		}
		// Iterate over all previous instances and delete old ones
		for ep := int(c.epoch) - limit; ep >= 0; ep-- {
			seed := seedHash(uint64(ep)*epochLength + 1)
			path := filepath.Join(dir, fmt.Sprintf("cache-R%d-%x%s", algorithmRevision, seed[:8], endian))
			os.Remove(path)
		}
	})
}

func (c *cache) finalizer() {
	if c.mmap != nil {
		c.mmap.Unmap()
		c.dump.Close()
		c.mmap, c.dump = nil, nil
	}
}

type dataset struct {
	epoch   uint64    // Epoch for which this cache is relevant
	dump    *os.File  // File descriptor of the memory mapped cache
	mmap    mmap.MMap // Memory map itself to unmap before releasing
	dataset []uint32  // The actual cache data content
	once    sync.Once // Ensures the cache is generated only once
}

func newDataset(epoch uint64) interface{} {
	return &dataset{epoch: epoch}
}

func (d *dataset) generate(dir string, limit int, test bool) {
	d.once.Do(func() {
		csize := cacheSize(d.epoch*epochLength + 1)
		dsize := datasetSize(d.epoch*epochLength + 1)
		seed := seedHash(d.epoch*epochLength + 1)
		if test {
			csize = 1024
			dsize = 32 * 1024
		}
		// If we don't store anything on disk, generate and return
		if dir == "" {
			cache := make([]uint32, csize/4)
			generateCache(cache, d.epoch, seed)

			d.dataset = make([]uint32, dsize/4)
			generateDataset(d.dataset, d.epoch, cache)
		}
		// Disk storage is needed, this will get fancy
		var endian string
		if !isLittleEndian() {
			endian = ".be"
		}
		path := filepath.Join(dir, fmt.Sprintf("full-R%d-%x%s", algorithmRevision, seed[:8], endian))
		logger := log4j.New("epoch", d.epoch)

		// We're about to mmap the file, ensure that the mapping is cleaned up when the
		// cache becomes unused.
		runtime.SetFinalizer(d, (*dataset).finalizer)

		// Try to load the file from disk and memory map it
		var err error
		d.dump, d.mmap, d.dataset, err = memoryMap(path)
		if err == nil {
			logger.Debug("Loaded old canhash dataset from disk")
			return
		}
		logger.Debug("Failed to load old canhash dataset", "err", err)

		// No previous dataset available, create a new dataset file to fill
		cache := make([]uint32, csize/4)
		generateCache(cache, d.epoch, seed)

		d.dump, d.mmap, d.dataset, err = memoryMapAndGenerate(path, dsize, func(buffer []uint32) { generateDataset(buffer, d.epoch, cache) })
		if err != nil {
			logger.Error("Failed to generate mapped canhash dataset", "err", err)

			d.dataset = make([]uint32, dsize/2)
			generateDataset(d.dataset, d.epoch, cache)
		}
		// Iterate over all previous instances and delete old ones
		for ep := int(d.epoch) - limit; ep >= 0; ep-- {
			seed := seedHash(uint64(ep)*epochLength + 1)
			path := filepath.Join(dir, fmt.Sprintf("full-R%d-%x%s", algorithmRevision, seed[:8], endian))
			os.Remove(path)
		}
	})
}

func (d *dataset) finalizer() {
	if d.mmap != nil {
		d.mmap.Unmap()
		d.dump.Close()
		d.mmap, d.dump = nil, nil
	}
}

func MakeCache(block uint64, dir string) {
	c := cache{epoch: block / epochLength}
	c.generate(dir, math.MaxInt32, false)
}

func MakeDataset(block uint64, dir string) {
	d := dataset{epoch: block / epochLength}
	d.generate(dir, math.MaxInt32, false)
}

type Mode uint

const (
	ModeNormal Mode = iota
	ModeShared
	ModeTest
	ModeFake
	ModeFullFake
)

type Config struct {
	CacheDir       string
	CachesInMem    int
	CachesOnDisk   int
	DatasetDir     string
	DatasetsInMem  int
	DatasetsOnDisk int
	PowMode        Mode
}

type Canhash struct {
	config Config

	caches   *lru // In memory caches to avoid regenerating too often
	datasets *lru // In memory datasets to avoid regenerating too often

	// Mining related fields
	rand     *rand.Rand    // Properly seeded random source for nonces
	threads  int           // Number of threads to mine on if mining
	update   chan struct{} // Notification channel to update mining parameters
	hashrate metrics.Meter // Meter tracking the average hashrate

	// The fields below are hooks for testing
	shared    *Canhash      // Shared PoW verifier to avoid cache regeneration
	fakeFail  uint64        // Block number which fails PoW check even in fake mode
	fakeDelay time.Duration // Time delay to sleep for before returning from verify

	lock sync.Mutex // Ensures thread safety for the in-memory caches and mining fields
}

func New(config Config) *Canhash {
	if config.CachesInMem <= 0 {
		log4j.Warn("One canhash cache must always be in memory", "requested", config.CachesInMem)
		config.CachesInMem = 1
	}
	if config.CacheDir != "" && config.CachesOnDisk > 0 {
		log4j.Info("Disk storage enabled for canhash caches", "dir", config.CacheDir, "count", config.CachesOnDisk)
	}
	if config.DatasetDir != "" && config.DatasetsOnDisk > 0 {
		log4j.Info("Disk storage enabled for canhash DAGs", "dir", config.DatasetDir, "count", config.DatasetsOnDisk)
	}
	return &Canhash{
		config:   config,
		caches:   newlru("cache", config.CachesInMem, newCache),
		datasets: newlru("dataset", config.DatasetsInMem, newDataset),
		update:   make(chan struct{}),
		hashrate: metrics.NewMeter(),
	}
}

func NewTester() *Canhash {
	return New(Config{CachesInMem: 1, PowMode: ModeTest})
}

func NewFaker() *Canhash {
	return &Canhash{
		config: Config{
			PowMode: ModeFake,
		},
	}
}

func NewFakeFailer(fail uint64) *Canhash {
	return &Canhash{
		config: Config{
			PowMode: ModeFake,
		},
		fakeFail: fail,
	}
}

func NewFakeDelayer(delay time.Duration) *Canhash {
	return &Canhash{
		config: Config{
			PowMode: ModeFake,
		},
		fakeDelay: delay,
	}
}

func NewFullFaker() *Canhash {
	return &Canhash{
		config: Config{
			PowMode: ModeFullFake,
		},
	}
}

func NewShared() *Canhash {
	return &Canhash{shared: sharedCanhash}
}

func (canhash *Canhash) cache(block uint64) *cache {
	epoch := block / epochLength
	currentI, futureI := canhash.caches.get(epoch)
	current := currentI.(*cache)

	// Wait for generation finish.
	current.generate(canhash.config.CacheDir, canhash.config.CachesOnDisk, canhash.config.PowMode == ModeTest)

	// If we need a new future cache, now's a good time to regenerate it.
	if futureI != nil {
		future := futureI.(*cache)
		go future.generate(canhash.config.CacheDir, canhash.config.CachesOnDisk, canhash.config.PowMode == ModeTest)
	}
	return current
}

func (canhash *Canhash) dataset(block uint64) *dataset {
	epoch := block / epochLength
	currentI, futureI := canhash.datasets.get(epoch)
	current := currentI.(*dataset)

	// Wait for generation finish.
	current.generate(canhash.config.DatasetDir, canhash.config.DatasetsOnDisk, canhash.config.PowMode == ModeTest)

	// If we need a new future dataset, now's a good time to regenerate it.
	if futureI != nil {
		future := futureI.(*dataset)
		go future.generate(canhash.config.DatasetDir, canhash.config.DatasetsOnDisk, canhash.config.PowMode == ModeTest)
	}

	return current
}

func (canhash *Canhash) Threads() int {
	canhash.lock.Lock()
	defer canhash.lock.Unlock()

	return canhash.threads
}

func (canhash *Canhash) SetThreads(threads int) {
	canhash.lock.Lock()
	defer canhash.lock.Unlock()

	// If we're running a shared PoW, set the thread count on that instead
	if canhash.shared != nil {
		canhash.shared.SetThreads(threads)
		return
	}
	// Update the threads and ping any running seal to pull in any changes
	canhash.threads = threads
	select {
	case canhash.update <- struct{}{}:
	default:
	}
}

func (canhash *Canhash) Hashrate() float64 {
	return canhash.hashrate.Rate1()
}

func (canhash *Canhash) APIs(chain consensus.ChainReader) []rpc.API {
	return nil
}

func SeedHash(block uint64) []byte {
	return seedHash(block)
}
