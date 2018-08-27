package pipes

import (
	"net"
)

func NetPipe() (net.Conn, net.Conn, error) {
	p1, p2 := net.Pipe()
	return p1, p2, nil
}

func TCPPipe() (net.Conn, net.Conn, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	defer l.Close()

	var aconn net.Conn
	aerr := make(chan error, 1)
	go func() {
		var err error
		aconn, err = l.Accept()
		aerr <- err
	}()

	dconn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		<-aerr
		return nil, nil, err
	}
	if err := <-aerr; err != nil {
		dconn.Close()
		return nil, nil, err
	}
	return aconn, dconn, nil
}
