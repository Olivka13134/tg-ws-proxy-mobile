package proxy

import (
	"net"

	"golang.org/x/sys/unix"
)

// macOS / iOS: TCP_CONNECTION_INFO. Data stays in the send buffer until the
// peer ACKs it, so a non-empty buffer that never shrinks means a dead path.
// progress = first-time transmitted bytes minus what is still buffered; it
// grows when the peer acknowledges data.
func probeFor(c net.Conn) tcpProgress {
	rc := rawConnOf(c)
	if rc == nil {
		return nil
	}
	return func() (bool, uint64, bool) {
		var info *unix.TCPConnectionInfo
		var err error
		cerr := rc.Control(func(fd uintptr) {
			info, err = unix.GetsockoptTCPConnectionInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_CONNECTION_INFO)
		})
		if cerr != nil || err != nil || info == nil {
			return false, 0, false
		}
		first := info.Txbytes - info.Txretransmitbytes
		buf := uint64(info.Snd_sbbytes)
		var prog uint64
		if first > buf {
			prog = first - buf
		}
		return buf > 0, prog, true
	}
}
