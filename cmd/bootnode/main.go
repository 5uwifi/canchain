package main

import (
	"crypto/ecdsa"
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/5uwifi/canchain/cmd/utils"
	"github.com/5uwifi/canchain/lib/crypto"
	"github.com/5uwifi/canchain/lib/log4j"
	"github.com/5uwifi/canchain/lib/p2p/cnode"
	"github.com/5uwifi/canchain/lib/p2p/discover"
	"github.com/5uwifi/canchain/lib/p2p/discv5"
	"github.com/5uwifi/canchain/lib/p2p/nat"
	"github.com/5uwifi/canchain/lib/p2p/netutil"
)

func main() {
	var (
		listenAddr  = flag.String("addr", ":30301", "listen address")
		genKey      = flag.String("genkey", "", "generate a node key")
		writeAddr   = flag.Bool("writeaddress", false, "write out the node's pubkey hash and quit")
		nodeKeyFile = flag.String("nodekey", "", "private key filename")
		nodeKeyHex  = flag.String("nodekeyhex", "", "private key as hex (for testing)")
		natdesc     = flag.String("nat", "none", "port mapping mechanism (any|none|upnp|pmp|extip:<IP>)")
		netrestrict = flag.String("netrestrict", "", "restrict network communication to the given IP networks (CIDR masks)")
		runv5       = flag.Bool("v5", false, "run a v5 topic discovery bootnode")
		verbosity   = flag.Int("verbosity", int(log4j.LvlInfo), "log verbosity (0-9)")
		vmodule     = flag.String("vmodule", "", "log verbosity pattern")

		nodeKey *ecdsa.PrivateKey
		err     error
	)
	flag.Parse()

	glogger := log4j.NewGlogHandler(log4j.StreamHandler(os.Stderr, log4j.TerminalFormat(false)))
	glogger.Verbosity(log4j.Lvl(*verbosity))
	glogger.Vmodule(*vmodule)
	log4j.Root().SetHandler(glogger)

	natm, err := nat.Parse(*natdesc)
	if err != nil {
		utils.Fatalf("-nat: %v", err)
	}
	switch {
	case *genKey != "":
		nodeKey, err = crypto.GenerateKey()
		if err != nil {
			utils.Fatalf("could not generate key: %v", err)
		}
		if err = crypto.SaveECDSA(*genKey, nodeKey); err != nil {
			utils.Fatalf("%v", err)
		}
		return
	case *nodeKeyFile == "" && *nodeKeyHex == "":
		utils.Fatalf("Use -nodekey or -nodekeyhex to specify a private key")
	case *nodeKeyFile != "" && *nodeKeyHex != "":
		utils.Fatalf("Options -nodekey and -nodekeyhex are mutually exclusive")
	case *nodeKeyFile != "":
		if nodeKey, err = crypto.LoadECDSA(*nodeKeyFile); err != nil {
			utils.Fatalf("-nodekey: %v", err)
		}
	case *nodeKeyHex != "":
		if nodeKey, err = crypto.HexToECDSA(*nodeKeyHex); err != nil {
			utils.Fatalf("-nodekeyhex: %v", err)
		}
	}

	if *writeAddr {
		fmt.Printf("%v\n", cnode.PubkeyToIDV4(&nodeKey.PublicKey))
		os.Exit(0)
	}

	var restrictList *netutil.Netlist
	if *netrestrict != "" {
		restrictList, err = netutil.ParseNetlist(*netrestrict)
		if err != nil {
			utils.Fatalf("-netrestrict: %v", err)
		}
	}

	addr, err := net.ResolveUDPAddr("udp", *listenAddr)
	if err != nil {
		utils.Fatalf("-ResolveUDPAddr: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		utils.Fatalf("-ListenUDP: %v", err)
	}

	realaddr := conn.LocalAddr().(*net.UDPAddr)
	if natm != nil {
		if !realaddr.IP.IsLoopback() {
			go nat.Map(natm, nil, "udp", realaddr.Port, realaddr.Port, "canchain discovery")
		}
		if ext, err := natm.ExternalIP(); err == nil {
			realaddr = &net.UDPAddr{IP: ext, Port: realaddr.Port}
		}
	}

	if *runv5 {
		if _, err := discv5.ListenUDP(nodeKey, conn, realaddr, "", restrictList); err != nil {
			utils.Fatalf("%v", err)
		}
	} else {
		cfg := discover.Config{
			PrivateKey:   nodeKey,
			AnnounceAddr: realaddr,
			NetRestrict:  restrictList,
		}
		if _, err := discover.ListenUDP(conn, cfg); err != nil {
			utils.Fatalf("%v", err)
		}
	}

	select {}
}
