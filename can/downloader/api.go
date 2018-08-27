package downloader

import (
	"context"
	"sync"

	"github.com/5uwifi/canchain"
	"github.com/5uwifi/canchain/lib/event"
	"github.com/5uwifi/canchain/rpc"
)

type PublicDownloaderAPI struct {
	d                         *Downloader
	mux                       *event.TypeMux
	installSyncSubscription   chan chan interface{}
	uninstallSyncSubscription chan *uninstallSyncSubscriptionRequest
}

func NewPublicDownloaderAPI(d *Downloader, m *event.TypeMux) *PublicDownloaderAPI {
	api := &PublicDownloaderAPI{
		d:   d,
		mux: m,
		installSyncSubscription:   make(chan chan interface{}),
		uninstallSyncSubscription: make(chan *uninstallSyncSubscriptionRequest),
	}

	go api.eventLoop()

	return api
}

func (api *PublicDownloaderAPI) eventLoop() {
	var (
		sub               = api.mux.Subscribe(StartEvent{}, DoneEvent{}, FailedEvent{})
		syncSubscriptions = make(map[chan interface{}]struct{})
	)

	for {
		select {
		case i := <-api.installSyncSubscription:
			syncSubscriptions[i] = struct{}{}
		case u := <-api.uninstallSyncSubscription:
			delete(syncSubscriptions, u.c)
			close(u.uninstalled)
		case event := <-sub.Chan():
			if event == nil {
				return
			}

			var notification interface{}
			switch event.Data.(type) {
			case StartEvent:
				notification = &SyncingResult{
					Syncing: true,
					Status:  api.d.Progress(),
				}
			case DoneEvent, FailedEvent:
				notification = false
			}
			for c := range syncSubscriptions {
				c <- notification
			}
		}
	}
}

func (api *PublicDownloaderAPI) Syncing(ctx context.Context) (*rpc.Subscription, error) {
	notifier, supported := rpc.NotifierFromContext(ctx)
	if !supported {
		return &rpc.Subscription{}, rpc.ErrNotificationsUnsupported
	}

	rpcSub := notifier.CreateSubscription()

	go func() {
		statuses := make(chan interface{})
		sub := api.SubscribeSyncStatus(statuses)

		for {
			select {
			case status := <-statuses:
				notifier.Notify(rpcSub.ID, status)
			case <-rpcSub.Err():
				sub.Unsubscribe()
				return
			case <-notifier.Closed():
				sub.Unsubscribe()
				return
			}
		}
	}()

	return rpcSub, nil
}

type SyncingResult struct {
	Syncing bool                  `json:"syncing"`
	Status  canchain.SyncProgress `json:"status"`
}

type uninstallSyncSubscriptionRequest struct {
	c           chan interface{}
	uninstalled chan interface{}
}

type SyncStatusSubscription struct {
	api       *PublicDownloaderAPI
	c         chan interface{}
	unsubOnce sync.Once
}

func (s *SyncStatusSubscription) Unsubscribe() {
	s.unsubOnce.Do(func() {
		req := uninstallSyncSubscriptionRequest{s.c, make(chan interface{})}
		s.api.uninstallSyncSubscription <- &req

		for {
			select {
			case <-s.c:
				continue
			case <-req.uninstalled:
				return
			}
		}
	})
}

func (api *PublicDownloaderAPI) SubscribeSyncStatus(status chan interface{}) *SyncStatusSubscription {
	api.installSyncSubscription <- status
	return &SyncStatusSubscription{api: api, c: status}
}
