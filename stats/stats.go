package stats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/5uwifi/canchain/can"
	"github.com/5uwifi/canchain/common"
	"github.com/5uwifi/canchain/common/mclock"
	"github.com/5uwifi/canchain/kernel"
	"github.com/5uwifi/canchain/kernel/types"
	"github.com/5uwifi/canchain/lcs"
	"github.com/5uwifi/canchain/lib/consensus"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p"
	"github.com/5uwifi/canchain/rpc"
	"golang.org/x/net/websocket"
)

const (
	historyUpdateRange = 50

	txChanSize        = 4096
	chainHeadChanSize = 10
)

type txPool interface {
	SubscribeNewTxsEvent(chan<- kernel.NewTxsEvent) event.Subscription
}

type blockChain interface {
	SubscribeChainHeadEvent(ch chan<- kernel.ChainHeadEvent) event.Subscription
}

type Service struct {
	server *p2p.Server
	can    *can.CANChain
	lcs    *lcs.LightCANChain
	engine consensus.Engine

	node string
	pass string
	host string

	pongCh chan struct{}
	histCh chan []uint64
}

func New(url string, canServ *can.CANChain, lesServ *lcs.LightCANChain) (*Service, error) {
	re := regexp.MustCompile("([^:@]*)(:([^@]*))?@(.+)")
	parts := re.FindStringSubmatch(url)
	if len(parts) != 5 {
		return nil, fmt.Errorf("invalid netstats url: \"%s\", should be nodename:secret@host:port", url)
	}
	var engine consensus.Engine
	if canServ != nil {
		engine = canServ.Engine()
	} else {
		engine = lesServ.Engine()
	}
	return &Service{
		can:    canServ,
		lcs:    lesServ,
		engine: engine,
		node:   parts[1],
		pass:   parts[3],
		host:   parts[4],
		pongCh: make(chan struct{}),
		histCh: make(chan []uint64, 1),
	}, nil
}

func (s *Service) Protocols() []p2p.Protocol { return nil }

func (s *Service) APIs() []rpc.API { return nil }

func (s *Service) Start(server *p2p.Server) error {
	s.server = server
	go s.loop()

	log4j.Info("Stats daemon started")
	return nil
}

func (s *Service) Stop() error {
	log4j.Info("Stats daemon stopped")
	return nil
}

func (s *Service) loop() {
	var blockchain blockChain
	var txpool txPool
	if s.can != nil {
		blockchain = s.can.BlockChain()
		txpool = s.can.TxPool()
	} else {
		blockchain = s.lcs.BlockChain()
		txpool = s.lcs.TxPool()
	}

	chainHeadCh := make(chan kernel.ChainHeadEvent, chainHeadChanSize)
	headSub := blockchain.SubscribeChainHeadEvent(chainHeadCh)
	defer headSub.Unsubscribe()

	txEventCh := make(chan kernel.NewTxsEvent, txChanSize)
	txSub := txpool.SubscribeNewTxsEvent(txEventCh)
	defer txSub.Unsubscribe()

	var (
		quitCh = make(chan struct{})
		headCh = make(chan *types.Block, 1)
		txCh   = make(chan struct{}, 1)
	)
	go func() {
		var lastTx mclock.AbsTime

	HandleLoop:
		for {
			select {
			case head := <-chainHeadCh:
				select {
				case headCh <- head.Block:
				default:
				}

			case <-txEventCh:
				if time.Duration(mclock.Now()-lastTx) < time.Second {
					continue
				}
				lastTx = mclock.Now()

				select {
				case txCh <- struct{}{}:
				default:
				}

			case <-txSub.Err():
				break HandleLoop
			case <-headSub.Err():
				break HandleLoop
			}
		}
		close(quitCh)
	}()
	for {
		path := fmt.Sprintf("%s/api", s.host)
		urls := []string{path}

		if !strings.Contains(path, "://") {
			urls = []string{"wss://" + path, "ws://" + path}
		}
		var (
			conf *websocket.Config
			conn *websocket.Conn
			err  error
		)
		for _, url := range urls {
			if conf, err = websocket.NewConfig(url, "http://localhost/"); err != nil {
				continue
			}
			conf.Dialer = &net.Dialer{Timeout: 5 * time.Second}
			if conn, err = websocket.DialConfig(conf); err == nil {
				break
			}
		}
		if err != nil {
			log4j.Warn("Stats server unreachable", "err", err)
			time.Sleep(10 * time.Second)
			continue
		}
		if err = s.login(conn); err != nil {
			log4j.Warn("Stats login failed", "err", err)
			conn.Close()
			time.Sleep(10 * time.Second)
			continue
		}
		go s.readLoop(conn)

		if err = s.report(conn); err != nil {
			log4j.Warn("Initial stats report failed", "err", err)
			conn.Close()
			continue
		}
		fullReport := time.NewTicker(15 * time.Second)

		for err == nil {
			select {
			case <-quitCh:
				conn.Close()
				return

			case <-fullReport.C:
				if err = s.report(conn); err != nil {
					log4j.Warn("Full stats report failed", "err", err)
				}
			case list := <-s.histCh:
				if err = s.reportHistory(conn, list); err != nil {
					log4j.Warn("Requested history report failed", "err", err)
				}
			case head := <-headCh:
				if err = s.reportBlock(conn, head); err != nil {
					log4j.Warn("Block stats report failed", "err", err)
				}
				if err = s.reportPending(conn); err != nil {
					log4j.Warn("Post-block transaction stats report failed", "err", err)
				}
			case <-txCh:
				if err = s.reportPending(conn); err != nil {
					log4j.Warn("Transaction stats report failed", "err", err)
				}
			}
		}
		conn.Close()
	}
}

func (s *Service) readLoop(conn *websocket.Conn) {
	defer conn.Close()

	for {
		var msg map[string][]interface{}
		if err := websocket.JSON.Receive(conn, &msg); err != nil {
			log4j.Warn("Failed to decode stats server message", "err", err)
			return
		}
		log4j.Trace("Received message from stats server", "msg", msg)
		if len(msg["emit"]) == 0 {
			log4j.Warn("Stats server sent non-broadcast", "msg", msg)
			return
		}
		command, ok := msg["emit"][0].(string)
		if !ok {
			log4j.Warn("Invalid stats server message type", "type", msg["emit"][0])
			return
		}
		if len(msg["emit"]) == 2 && command == "node-pong" {
			select {
			case s.pongCh <- struct{}{}:
				continue
			default:
				log4j.Warn("Stats server pinger seems to have died")
				return
			}
		}
		if len(msg["emit"]) == 2 && command == "history" {
			request, ok := msg["emit"][1].(map[string]interface{})
			if !ok {
				log4j.Warn("Invalid stats history request", "msg", msg["emit"][1])
				s.histCh <- nil
				continue
			}
			list, ok := request["list"].([]interface{})
			if !ok {
				log4j.Warn("Invalid stats history block list", "list", request["list"])
				return
			}
			numbers := make([]uint64, len(list))
			for i, num := range list {
				n, ok := num.(float64)
				if !ok {
					log4j.Warn("Invalid stats history block number", "number", num)
					return
				}
				numbers[i] = uint64(n)
			}
			select {
			case s.histCh <- numbers:
				continue
			default:
			}
		}
		log4j.Info("Unknown stats message", "msg", msg)
	}
}

type nodeInfo struct {
	Name     string `json:"name"`
	Node     string `json:"node"`
	Port     int    `json:"port"`
	Network  string `json:"net"`
	Protocol string `json:"protocol"`
	API      string `json:"api"`
	Os       string `json:"os"`
	OsVer    string `json:"os_v"`
	Client   string `json:"client"`
	History  bool   `json:"canUpdateHistory"`
}

type authMsg struct {
	ID     string   `json:"id"`
	Info   nodeInfo `json:"info"`
	Secret string   `json:"secret"`
}

func (s *Service) login(conn *websocket.Conn) error {
	infos := s.server.NodeInfo()

	var network, protocol string
	if info := infos.Protocols["eth"]; info != nil {
		network = fmt.Sprintf("%d", info.(*can.NodeInfo).Network)
		protocol = fmt.Sprintf("eth/%d", can.ProtocolVersions[0])
	} else {
		network = fmt.Sprintf("%d", infos.Protocols["les"].(*lcs.NodeInfo).Network)
		protocol = fmt.Sprintf("les/%d", lcs.ClientProtocolVersions[0])
	}
	auth := &authMsg{
		ID: s.node,
		Info: nodeInfo{
			Name:     s.node,
			Node:     infos.Name,
			Port:     infos.Ports.Listener,
			Network:  network,
			Protocol: protocol,
			API:      "No",
			Os:       runtime.GOOS,
			OsVer:    runtime.GOARCH,
			Client:   "0.1.1",
			History:  true,
		},
		Secret: s.pass,
	}
	login := map[string][]interface{}{
		"emit": {"hello", auth},
	}
	if err := websocket.JSON.Send(conn, login); err != nil {
		return err
	}
	var ack map[string][]string
	if err := websocket.JSON.Receive(conn, &ack); err != nil || len(ack["emit"]) != 1 || ack["emit"][0] != "ready" {
		return errors.New("unauthorized")
	}
	return nil
}

func (s *Service) report(conn *websocket.Conn) error {
	if err := s.reportLatency(conn); err != nil {
		return err
	}
	if err := s.reportBlock(conn, nil); err != nil {
		return err
	}
	if err := s.reportPending(conn); err != nil {
		return err
	}
	if err := s.reportStats(conn); err != nil {
		return err
	}
	return nil
}

func (s *Service) reportLatency(conn *websocket.Conn) error {
	start := time.Now()

	ping := map[string][]interface{}{
		"emit": {"node-ping", map[string]string{
			"id":         s.node,
			"clientTime": start.String(),
		}},
	}
	if err := websocket.JSON.Send(conn, ping); err != nil {
		return err
	}
	select {
	case <-s.pongCh:
	case <-time.After(5 * time.Second):
		return errors.New("ping timed out")
	}
	latency := strconv.Itoa(int((time.Since(start) / time.Duration(2)).Nanoseconds() / 1000000))

	log4j.Trace("Sending measured latency to ethstats", "latency", latency)

	stats := map[string][]interface{}{
		"emit": {"latency", map[string]string{
			"id":      s.node,
			"latency": latency,
		}},
	}
	return websocket.JSON.Send(conn, stats)
}

type blockStats struct {
	Number     *big.Int       `json:"number"`
	Hash       common.Hash    `json:"hash"`
	ParentHash common.Hash    `json:"parentHash"`
	Timestamp  *big.Int       `json:"timestamp"`
	Miner      common.Address `json:"miner"`
	GasUsed    uint64         `json:"gasUsed"`
	GasLimit   uint64         `json:"gasLimit"`
	Diff       string         `json:"difficulty"`
	TotalDiff  string         `json:"totalDifficulty"`
	Txs        []txStats      `json:"transactions"`
	TxHash     common.Hash    `json:"transactionsRoot"`
	Root       common.Hash    `json:"stateRoot"`
	Uncles     uncleStats     `json:"uncles"`
}

type txStats struct {
	Hash common.Hash `json:"hash"`
}

type uncleStats []*types.Header

func (s uncleStats) MarshalJSON() ([]byte, error) {
	if uncles := ([]*types.Header)(s); len(uncles) > 0 {
		return json.Marshal(uncles)
	}
	return []byte("[]"), nil
}

func (s *Service) reportBlock(conn *websocket.Conn, block *types.Block) error {
	details := s.assembleBlockStats(block)

	log4j.Trace("Sending new block to ethstats", "number", details.Number, "hash", details.Hash)

	stats := map[string]interface{}{
		"id":    s.node,
		"block": details,
	}
	report := map[string][]interface{}{
		"emit": {"block", stats},
	}
	return websocket.JSON.Send(conn, report)
}

func (s *Service) assembleBlockStats(block *types.Block) *blockStats {
	var (
		header *types.Header
		td     *big.Int
		txs    []txStats
		uncles []*types.Header
	)
	if s.can != nil {
		if block == nil {
			block = s.can.BlockChain().CurrentBlock()
		}
		header = block.Header()
		td = s.can.BlockChain().GetTd(header.Hash(), header.Number.Uint64())

		txs = make([]txStats, len(block.Transactions()))
		for i, tx := range block.Transactions() {
			txs[i].Hash = tx.Hash()
		}
		uncles = block.Uncles()
	} else {
		if block != nil {
			header = block.Header()
		} else {
			header = s.lcs.BlockChain().CurrentHeader()
		}
		td = s.lcs.BlockChain().GetTd(header.Hash(), header.Number.Uint64())
		txs = []txStats{}
	}
	author, _ := s.engine.Author(header)

	return &blockStats{
		Number:     header.Number,
		Hash:       header.Hash(),
		ParentHash: header.ParentHash,
		Timestamp:  header.Time,
		Miner:      author,
		GasUsed:    header.GasUsed,
		GasLimit:   header.GasLimit,
		Diff:       header.Difficulty.String(),
		TotalDiff:  td.String(),
		Txs:        txs,
		TxHash:     header.TxHash,
		Root:       header.Root,
		Uncles:     uncles,
	}
}

func (s *Service) reportHistory(conn *websocket.Conn, list []uint64) error {
	indexes := make([]uint64, 0, historyUpdateRange)
	if len(list) > 0 {
		indexes = append(indexes, list...)
	} else {
		var head int64
		if s.can != nil {
			head = s.can.BlockChain().CurrentHeader().Number.Int64()
		} else {
			head = s.lcs.BlockChain().CurrentHeader().Number.Int64()
		}
		start := head - historyUpdateRange + 1
		if start < 0 {
			start = 0
		}
		for i := uint64(start); i <= uint64(head); i++ {
			indexes = append(indexes, i)
		}
	}
	history := make([]*blockStats, len(indexes))
	for i, number := range indexes {
		var block *types.Block
		if s.can != nil {
			block = s.can.BlockChain().GetBlockByNumber(number)
		} else {
			if header := s.lcs.BlockChain().GetHeaderByNumber(number); header != nil {
				block = types.NewBlockWithHeader(header)
			}
		}
		if block != nil {
			history[len(history)-1-i] = s.assembleBlockStats(block)
			continue
		}
		history = history[len(history)-i:]
		break
	}
	if len(history) > 0 {
		log4j.Trace("Sending historical blocks to ethstats", "first", history[0].Number, "last", history[len(history)-1].Number)
	} else {
		log4j.Trace("No history to send to stats server")
	}
	stats := map[string]interface{}{
		"id":      s.node,
		"history": history,
	}
	report := map[string][]interface{}{
		"emit": {"history", stats},
	}
	return websocket.JSON.Send(conn, report)
}

type pendStats struct {
	Pending int `json:"pending"`
}

func (s *Service) reportPending(conn *websocket.Conn) error {
	var pending int
	if s.can != nil {
		pending, _ = s.can.TxPool().Stats()
	} else {
		pending = s.lcs.TxPool().Stats()
	}
	log4j.Trace("Sending pending transactions to ethstats", "count", pending)

	stats := map[string]interface{}{
		"id": s.node,
		"stats": &pendStats{
			Pending: pending,
		},
	}
	report := map[string][]interface{}{
		"emit": {"pending", stats},
	}
	return websocket.JSON.Send(conn, report)
}

type nodeStats struct {
	Active   bool `json:"active"`
	Syncing  bool `json:"syncing"`
	Mining   bool `json:"mining"`
	Hashrate int  `json:"hashrate"`
	Peers    int  `json:"peers"`
	GasPrice int  `json:"gasPrice"`
	Uptime   int  `json:"uptime"`
}

func (s *Service) reportStats(conn *websocket.Conn) error {
	var (
		mining   bool
		hashrate int
		syncing  bool
		gasprice int
	)
	if s.can != nil {
		mining = s.can.Miner().Mining()
		hashrate = int(s.can.Miner().HashRate())

		sync := s.can.Downloader().Progress()
		syncing = s.can.BlockChain().CurrentHeader().Number.Uint64() >= sync.HighestBlock

		price, _ := s.can.APIBackend.SuggestPrice(context.Background())
		gasprice = int(price.Uint64())
	} else {
		sync := s.lcs.Downloader().Progress()
		syncing = s.lcs.BlockChain().CurrentHeader().Number.Uint64() >= sync.HighestBlock
	}
	log4j.Trace("Sending node details to ethstats")

	stats := map[string]interface{}{
		"id": s.node,
		"stats": &nodeStats{
			Active:   true,
			Mining:   mining,
			Hashrate: hashrate,
			Peers:    s.server.PeerCount(),
			GasPrice: gasprice,
			Syncing:  syncing,
			Uptime:   100,
		},
	}
	report := map[string][]interface{}{
		"emit": {"stats", stats},
	}
	return websocket.JSON.Send(conn, report)
}
