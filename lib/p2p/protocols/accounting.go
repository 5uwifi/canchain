package protocols

import "github.com/5uwifi/canchain/lib/metrics"

var (
	mBalanceCredit = metrics.NewRegisteredCounterForced("account.balance.credit", nil)
	mBalanceDebit  = metrics.NewRegisteredCounterForced("account.balance.debit", nil)
	mBytesCredit   = metrics.NewRegisteredCounterForced("account.bytes.credit", nil)
	mBytesDebit    = metrics.NewRegisteredCounterForced("account.bytes.debit", nil)
	mMsgCredit     = metrics.NewRegisteredCounterForced("account.msg.credit", nil)
	mMsgDebit      = metrics.NewRegisteredCounterForced("account.msg.debit", nil)
	mPeerDrops     = metrics.NewRegisteredCounterForced("account.peerdrops", nil)
	mSelfDrops     = metrics.NewRegisteredCounterForced("account.selfdrops", nil)
)

type Prices interface {
	Price(interface{}) *Price
}

type Payer bool

const (
	Sender   = Payer(true)
	Receiver = Payer(false)
)

type Price struct {
	Value   uint64
	PerByte bool
	Payer   Payer
}

func (p *Price) For(payer Payer, size uint32) int64 {
	price := p.Value
	if p.PerByte {
		price *= uint64(size)
	}
	if p.Payer == payer {
		return 0 - int64(price)
	}
	return int64(price)
}

type Balance interface {
	Add(amount int64, peer *Peer) error
}

type Accounting struct {
	Balance
	Prices
}

func NewAccounting(balance Balance, po Prices) *Accounting {
	ah := &Accounting{
		Prices:  po,
		Balance: balance,
	}
	return ah
}

func (ah *Accounting) Send(peer *Peer, size uint32, msg interface{}) error {
	price := ah.Price(msg)
	if price == nil {
		return nil
	}
	costToLocalNode := price.For(Sender, size)
	err := ah.Add(costToLocalNode, peer)
	ah.doMetrics(costToLocalNode, size, err)
	return err
}

func (ah *Accounting) Receive(peer *Peer, size uint32, msg interface{}) error {
	price := ah.Price(msg)
	if price == nil {
		return nil
	}
	costToLocalNode := price.For(Receiver, size)
	err := ah.Add(costToLocalNode, peer)
	ah.doMetrics(costToLocalNode, size, err)
	return err
}

func (ah *Accounting) doMetrics(price int64, size uint32, err error) {
	if price > 0 {
		mBalanceCredit.Inc(price)
		mBytesCredit.Inc(int64(size))
		mMsgCredit.Inc(1)
		if err != nil {
			mPeerDrops.Inc(1)
		}
	} else {
		mBalanceDebit.Inc(price)
		mBytesDebit.Inc(int64(size))
		mMsgDebit.Inc(1)
		if err != nil {
			mSelfDrops.Inc(1)
		}
	}
}
