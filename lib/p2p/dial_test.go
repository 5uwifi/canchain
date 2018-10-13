package p2p

import (
	"encoding/binary"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/enr"
	"github.com/5uwifi/canchain/lib/p2p/netutil"
)

func init() {
	spew.Config.Indent = "\t"
}

type dialtest struct {
	init   *dialstate
	rounds []round
}

type round struct {
	peers []*Peer
	done  []task
	new   []task
}

func runDialTest(t *testing.T, test dialtest) {
	var (
		vtime   time.Time
		running int
	)
	pm := func(ps []*Peer) map[cnode.ID]*Peer {
		m := make(map[cnode.ID]*Peer)
		for _, p := range ps {
			m[p.ID()] = p
		}
		return m
	}
	for i, round := range test.rounds {
		for _, task := range round.done {
			running--
			if running < 0 {
				panic("running task counter underflow")
			}
			test.init.taskDone(task, vtime)
		}

		new := test.init.newTasks(running, pm(round.peers), vtime)
		if !sametasks(new, round.new) {
			t.Errorf("round %d: new tasks mismatch:\ngot %v\nwant %v\nstate: %v\nrunning: %v\n",
				i, spew.Sdump(new), spew.Sdump(round.new), spew.Sdump(test.init), spew.Sdump(running))
		}
		t.Log("tasks:", spew.Sdump(new))

		vtime = vtime.Add(16 * time.Second)
		running += len(new)
	}
}

type fakeTable []*cnode.Node

func (t fakeTable) Self() *cnode.Node                     { return new(cnode.Node) }
func (t fakeTable) Close()                                {}
func (t fakeTable) LookupRandom() []*cnode.Node           { return nil }
func (t fakeTable) Resolve(*cnode.Node) *cnode.Node       { return nil }
func (t fakeTable) ReadRandomNodes(buf []*cnode.Node) int { return copy(buf, t) }

func TestDialStateDynDial(t *testing.T) {
	runDialTest(t, dialtest{
		init: newDialState(cnode.ID{}, nil, nil, fakeTable{}, 5, nil),
		rounds: []round{
			{
				peers: []*Peer{
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(0), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
				},
				new: []task{&discoverTask{}},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(0), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
				},
				done: []task{
					&discoverTask{results: []*cnode.Node{
						newNode(uintID(2), nil),
						newNode(uintID(3), nil),
						newNode(uintID(4), nil),
						newNode(uintID(5), nil),
						newNode(uintID(6), nil),
						newNode(uintID(7), nil),
					}},
				},
				new: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(3), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(4), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(5), nil)},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(0), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(3), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(4), nil)}},
				},
				done: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(3), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(4), nil)},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(0), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(3), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(4), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(5), nil)}},
				},
				done: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(5), nil)},
				},
				new: []task{
					&waitExpireTask{Duration: 14 * time.Second},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(0), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(3), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(4), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(5), nil)}},
				},
				new: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(6), nil)},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(0), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(5), nil)}},
				},
				done: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(6), nil)},
				},
				new: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(7), nil)},
					&discoverTask{},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(0), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(5), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(7), nil)}},
				},
				done: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(7), nil)},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(0), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(5), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(7), nil)}},
				},
				done: []task{
					&discoverTask{},
				},
				new: []task{
					&discoverTask{},
				},
			},
		},
	})
}

func TestDialStateDynDialBootnode(t *testing.T) {
	bootnodes := []*cnode.Node{
		newNode(uintID(1), nil),
		newNode(uintID(2), nil),
		newNode(uintID(3), nil),
	}
	table := fakeTable{
		newNode(uintID(4), nil),
		newNode(uintID(5), nil),
		newNode(uintID(6), nil),
		newNode(uintID(7), nil),
		newNode(uintID(8), nil),
	}
	runDialTest(t, dialtest{
		init: newDialState(cnode.ID{}, nil, bootnodes, table, 5, nil),
		rounds: []round{
			{
				new: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(4), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(5), nil)},
					&discoverTask{},
				},
			},
			{
				done: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(4), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(5), nil)},
				},
			},
			{},
			{
				new: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(1), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(4), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(5), nil)},
				},
			},
			{
				done: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(1), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(4), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(5), nil)},
				},
				new: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(2), nil)},
				},
			},
			{
				done: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(2), nil)},
				},
				new: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(3), nil)},
				},
			},
			{
				done: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(3), nil)},
				},
				new: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(1), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(4), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(5), nil)},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(4), nil)}},
				},
				done: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(1), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(4), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(5), nil)},
				},
			},
		},
	})
}

func TestDialStateDynDialFromTable(t *testing.T) {
	table := fakeTable{
		newNode(uintID(1), nil),
		newNode(uintID(2), nil),
		newNode(uintID(3), nil),
		newNode(uintID(4), nil),
		newNode(uintID(5), nil),
		newNode(uintID(6), nil),
		newNode(uintID(7), nil),
		newNode(uintID(8), nil),
	}

	runDialTest(t, dialtest{
		init: newDialState(cnode.ID{}, nil, nil, table, 10, nil),
		rounds: []round{
			{
				new: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(1), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(2), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(3), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(4), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(5), nil)},
					&discoverTask{},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
				},
				done: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(1), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(2), nil)},
					&discoverTask{results: []*cnode.Node{
						newNode(uintID(10), nil),
						newNode(uintID(11), nil),
						newNode(uintID(12), nil),
					}},
				},
				new: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(10), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(11), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(12), nil)},
					&discoverTask{},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(10), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(11), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(12), nil)}},
				},
				done: []task{
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(3), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(4), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(5), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(10), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(11), nil)},
					&dialTask{flags: dynDialedConn, dest: newNode(uintID(12), nil)},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(10), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(11), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(12), nil)}},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(10), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(11), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(12), nil)}},
				},
			},
		},
	})
}

func newNode(id cnode.ID, ip net.IP) *cnode.Node {
	var r enr.Record
	if ip != nil {
		r.Set(enr.IP(ip))
	}
	return cnode.SignNull(&r, id)
}

func TestDialStateNetRestrict(t *testing.T) {
	table := fakeTable{
		newNode(uintID(1), net.ParseIP("127.0.0.1")),
		newNode(uintID(2), net.ParseIP("127.0.0.2")),
		newNode(uintID(3), net.ParseIP("127.0.0.3")),
		newNode(uintID(4), net.ParseIP("127.0.0.4")),
		newNode(uintID(5), net.ParseIP("127.0.2.5")),
		newNode(uintID(6), net.ParseIP("127.0.2.6")),
		newNode(uintID(7), net.ParseIP("127.0.2.7")),
		newNode(uintID(8), net.ParseIP("127.0.2.8")),
	}
	restrict := new(netutil.Netlist)
	restrict.Add("127.0.2.0/24")

	runDialTest(t, dialtest{
		init: newDialState(cnode.ID{}, nil, nil, table, 10, restrict),
		rounds: []round{
			{
				new: []task{
					&dialTask{flags: dynDialedConn, dest: table[4]},
					&discoverTask{},
				},
			},
		},
	})
}

func TestDialStateStaticDial(t *testing.T) {
	wantStatic := []*cnode.Node{
		newNode(uintID(1), nil),
		newNode(uintID(2), nil),
		newNode(uintID(3), nil),
		newNode(uintID(4), nil),
		newNode(uintID(5), nil),
	}

	runDialTest(t, dialtest{
		init: newDialState(cnode.ID{}, wantStatic, nil, fakeTable{}, 0, nil),
		rounds: []round{
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
				},
				new: []task{
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(3), nil)},
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(4), nil)},
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(5), nil)},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(3), nil)}},
				},
				done: []task{
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(3), nil)},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(3), nil)}},
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(4), nil)}},
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(5), nil)}},
				},
				done: []task{
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(4), nil)},
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(5), nil)},
				},
				new: []task{
					&waitExpireTask{Duration: 14 * time.Second},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(3), nil)}},
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(4), nil)}},
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(5), nil)}},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(3), nil)}},
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(5), nil)}},
				},
				new: []task{
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(2), nil)},
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(4), nil)},
				},
			},
		},
	})
}

func TestDialStaticAfterReset(t *testing.T) {
	wantStatic := []*cnode.Node{
		newNode(uintID(1), nil),
		newNode(uintID(2), nil),
	}

	rounds := []round{
		{
			peers: nil,
			new: []task{
				&dialTask{flags: staticDialedConn, dest: newNode(uintID(1), nil)},
				&dialTask{flags: staticDialedConn, dest: newNode(uintID(2), nil)},
			},
		},
		{
			peers: []*Peer{
				{rw: &conn{flags: staticDialedConn, node: newNode(uintID(1), nil)}},
				{rw: &conn{flags: staticDialedConn, node: newNode(uintID(2), nil)}},
			},
			done: []task{
				&dialTask{flags: staticDialedConn, dest: newNode(uintID(1), nil)},
				&dialTask{flags: staticDialedConn, dest: newNode(uintID(2), nil)},
			},
			new: []task{
				&waitExpireTask{Duration: 30 * time.Second},
			},
		},
	}
	dTest := dialtest{
		init:   newDialState(cnode.ID{}, wantStatic, nil, fakeTable{}, 0, nil),
		rounds: rounds,
	}
	runDialTest(t, dTest)
	for _, n := range wantStatic {
		dTest.init.removeStatic(n)
		dTest.init.addStatic(n)
	}
	runDialTest(t, dTest)
}

func TestDialStateCache(t *testing.T) {
	wantStatic := []*cnode.Node{
		newNode(uintID(1), nil),
		newNode(uintID(2), nil),
		newNode(uintID(3), nil),
	}

	runDialTest(t, dialtest{
		init: newDialState(cnode.ID{}, wantStatic, nil, fakeTable{}, 0, nil),
		rounds: []round{
			{
				peers: nil,
				new: []task{
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(1), nil)},
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(2), nil)},
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(3), nil)},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: staticDialedConn, node: newNode(uintID(2), nil)}},
				},
				done: []task{
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(1), nil)},
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(2), nil)},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
				},
				done: []task{
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(3), nil)},
				},
				new: []task{
					&waitExpireTask{Duration: 14 * time.Second},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
				},
			},
			{
				peers: []*Peer{
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(1), nil)}},
					{rw: &conn{flags: dynDialedConn, node: newNode(uintID(2), nil)}},
				},
				new: []task{
					&dialTask{flags: staticDialedConn, dest: newNode(uintID(3), nil)},
				},
			},
		},
	})
}

func TestDialResolve(t *testing.T) {
	resolved := newNode(uintID(1), net.IP{127, 0, 55, 234})
	table := &resolveMock{answer: resolved}
	state := newDialState(cnode.ID{}, nil, nil, table, 0, nil)

	dest := newNode(uintID(1), nil)
	state.addStatic(dest)
	tasks := state.newTasks(0, nil, time.Time{})
	if !reflect.DeepEqual(tasks, []task{&dialTask{flags: staticDialedConn, dest: dest}}) {
		t.Fatalf("expected dial task, got %#v", tasks)
	}

	config := Config{Dialer: TCPDialer{&net.Dialer{Deadline: time.Now().Add(-5 * time.Minute)}}}
	srv := &Server{ntab: table, Config: config}
	tasks[0].Do(srv)
	if !reflect.DeepEqual(table.resolveCalls, []*cnode.Node{dest}) {
		t.Fatalf("wrong resolve calls, got %v", table.resolveCalls)
	}

	state.taskDone(tasks[0], time.Now())
	if state.static[uintID(1)].dest != resolved {
		t.Fatalf("state.dest not updated")
	}
}

func sametasks(a, b []task) bool {
	if len(a) != len(b) {
		return false
	}
next:
	for _, ta := range a {
		for _, tb := range b {
			if reflect.DeepEqual(ta, tb) {
				continue next
			}
		}
		return false
	}
	return true
}

func uintID(i uint32) cnode.ID {
	var id cnode.ID
	binary.BigEndian.PutUint32(id[:], i)
	return id
}

type resolveMock struct {
	resolveCalls []*cnode.Node
	answer       *cnode.Node
}

func (t *resolveMock) Resolve(n *cnode.Node) *cnode.Node {
	t.resolveCalls = append(t.resolveCalls, n)
	return t.answer
}

func (t *resolveMock) Self() *cnode.Node                     { return new(cnode.Node) }
func (t *resolveMock) Close()                                {}
func (t *resolveMock) LookupRandom() []*cnode.Node           { return nil }
func (t *resolveMock) ReadRandomNodes(buf []*cnode.Node) int { return 0 }
