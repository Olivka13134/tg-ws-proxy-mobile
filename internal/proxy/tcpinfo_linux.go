package proxy

import (
	"net"

	"golang.org/x/sys/unix"
)

func probeFor(c net.Conn) tcpProgress {
	rc := rawConnOf(c)
	if rc == nil {
		return nil
	}
	return func() (bool, uint64, bool) {
		var info *unix.TCPInfo
		var err error
		cerr := rc.Control(func(fd uintptr) {
			info, err = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
		})
		if cerr != nil || err != nil || info == nil {
			return false, 0, false
		}
		return info.Unacked > 0 || info.Notsent_bytes > 0, info.Bytes_acked, true
	}
}
