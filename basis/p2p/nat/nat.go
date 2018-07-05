//
// (at your option) any later version.
//
//

package nat

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/jackpal/go-nat-pmp"
)

type Interface interface {
	// These methods manage a mapping between a port on the local
	// machine to a port that can be connected to from the internet.
	//
	// protocol is "UDP" or "TCP". Some implementations allow setting
	// a display name for the mapping. The mapping may be removed by
	// the gateway when its lifetime ends.
	AddMapping(protocol string, extport, intport int, name string, lifetime time.Duration) error
	DeleteMapping(protocol string, extport, intport int) error

	// This method should return the external (Internet-facing)
	// address of the gateway device.
	ExternalIP() (net.IP, error)

	// Should return name of the method. This is used for logging.
	String() string
}

//
//     "" or "none"         return nil
//     "extip:77.12.33.4"   will assume the local machine is reachable on the given IP
//     "any"                uses the first auto-detected mechanism
//     "upnp"               uses the Universal Plug and Play protocol
//     "pmp"                uses NAT-PMP with an auto-detected gateway address
//     "pmp:192.168.0.1"    uses NAT-PMP with the given gateway address
func Parse(spec string) (Interface, error) {
	var (
		parts = strings.SplitN(spec, ":", 2)
		mech  = strings.ToLower(parts[0])
		ip    net.IP
	)
	if len(parts) > 1 {
		ip = net.ParseIP(parts[1])
		if ip == nil {
			return nil, errors.New("invalid IP address")
		}
	}
	switch mech {
	case "", "none", "off":
		return nil, nil
	case "any", "auto", "on":
		return Any(), nil
	case "extip", "ip":
		if ip == nil {
			return nil, errors.New("missing IP address")
		}
		return ExtIP(ip), nil
	case "upnp":
		return UPnP(), nil
	case "pmp", "natpmp", "nat-pmp":
		return PMP(ip), nil
	default:
		return nil, fmt.Errorf("unknown mechanism %q", parts[0])
	}
}

const (
	mapTimeout        = 20 * time.Minute
	mapUpdateInterval = 15 * time.Minute
)

func Map(m Interface, c chan struct{}, protocol string, extport, intport int, name string) {
	log := log4j.New("proto", protocol, "extport", extport, "intport", intport, "interface", m)
	refresh := time.NewTimer(mapUpdateInterval)
	defer func() {
		refresh.Stop()
		log.Debug("Deleting port mapping")
		m.DeleteMapping(protocol, extport, intport)
	}()
	if err := m.AddMapping(protocol, extport, intport, name, mapTimeout); err != nil {
		log.Debug("Couldn't add port mapping", "err", err)
	} else {
		log.Info("Mapped network port")
	}
	for {
		select {
		case _, ok := <-c:
			if !ok {
				return
			}
		case <-refresh.C:
			log.Trace("Refreshing port mapping")
			if err := m.AddMapping(protocol, extport, intport, name, mapTimeout); err != nil {
				log.Debug("Couldn't add port mapping", "err", err)
			}
			refresh.Reset(mapUpdateInterval)
		}
	}
}

func ExtIP(ip net.IP) Interface {
	if ip == nil {
		panic("IP must not be nil")
	}
	return extIP(ip)
}

type extIP net.IP

func (n extIP) ExternalIP() (net.IP, error) { return net.IP(n), nil }
func (n extIP) String() string              { return fmt.Sprintf("ExtIP(%v)", net.IP(n)) }

func (extIP) AddMapping(string, int, int, string, time.Duration) error { return nil }
func (extIP) DeleteMapping(string, int, int) error                     { return nil }

func Any() Interface {
	// TODO: attempt to discover whether the local machine has an
	// Internet-class address. Return ExtIP in this case.
	return startautodisc("UPnP or NAT-PMP", func() Interface {
		found := make(chan Interface, 2)
		go func() { found <- discoverUPnP() }()
		go func() { found <- discoverPMP() }()
		for i := 0; i < cap(found); i++ {
			if c := <-found; c != nil {
				return c
			}
		}
		return nil
	})
}

func UPnP() Interface {
	return startautodisc("UPnP", discoverUPnP)
}

func PMP(gateway net.IP) Interface {
	if gateway != nil {
		return &pmp{gw: gateway, c: natpmp.NewClient(gateway)}
	}
	return startautodisc("NAT-PMP", discoverPMP)
}

//
type autodisc struct {
	what string // type of interface being autodiscovered
	once sync.Once
	doit func() Interface

	mu    sync.Mutex
	found Interface
}

func startautodisc(what string, doit func() Interface) Interface {
	// TODO: monitor network configuration and rerun doit when it changes.
	return &autodisc{what: what, doit: doit}
}

func (n *autodisc) AddMapping(protocol string, extport, intport int, name string, lifetime time.Duration) error {
	if err := n.wait(); err != nil {
		return err
	}
	return n.found.AddMapping(protocol, extport, intport, name, lifetime)
}

func (n *autodisc) DeleteMapping(protocol string, extport, intport int) error {
	if err := n.wait(); err != nil {
		return err
	}
	return n.found.DeleteMapping(protocol, extport, intport)
}

func (n *autodisc) ExternalIP() (net.IP, error) {
	if err := n.wait(); err != nil {
		return nil, err
	}
	return n.found.ExternalIP()
}

func (n *autodisc) String() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.found == nil {
		return n.what
	} else {
		return n.found.String()
	}
}

func (n *autodisc) wait() error {
	n.once.Do(func() {
		n.mu.Lock()
		n.found = n.doit()
		n.mu.Unlock()
	})
	if n.found == nil {
		return fmt.Errorf("no %s router discovered", n.what)
	}
	return nil
}
