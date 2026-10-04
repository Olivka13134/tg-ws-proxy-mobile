package proxy

import (
	"crypto/tls"
	"net"
	"syscall"
)

// tcpProgress reports whether the socket has unacknowledged outgoing data
// and a counter that grows whenever the peer acknowledges something.
// ok=false means the platform can't tell (watchdog disabled).
type tcpProgress func() (unacked bool, acked uint64, ok bool)

func rawConnOf(c net.Conn) syscall.RawConn {
	for {
		switch t := c.(type) {
		case *tls.Conn:
			c = t.NetConn()
		case *net.TCPConn:
			rc, err := t.SyscallConn()
			if err != nil {
				return nil
			}
			return rc
		default:
			return nil
		}
	}
}
