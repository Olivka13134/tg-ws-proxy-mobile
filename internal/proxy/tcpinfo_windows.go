package proxy

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TCP_INFO_v0 (Windows 10 1703+)
type tcpInfoV0 struct {
	State             uint32
	Mss               uint32
	ConnectionTimeMs  uint64
	TimestampsEnabled uint8
	_                 [3]byte
	RttUs             uint32
	MinRttUs          uint32
	BytesInFlight     uint32
	Cwnd              uint32
	SndWnd            uint32
	RcvWnd            uint32
	RcvBuf            uint32
	BytesOut          uint64
	BytesIn           uint64
	BytesReordered    uint32
	BytesRetrans      uint32
	FastRetrans       uint32
	DupAcksIn         uint32
	TimeoutEpisodes   uint32
	SynRetrans        uint8
	_                 [3]byte
}

const sioTCPInfo = 0xD8000027 // _WSAIORW(IOC_VENDOR, 39)

func probeFor(c net.Conn) tcpProgress {
	rc := rawConnOf(c)
	if rc == nil {
		return nil
	}
	return func() (bool, uint64, bool) {
		var info tcpInfoV0
		var ret uint32
		var err error
		ver := uint32(0)
		cerr := rc.Control(func(fd uintptr) {
			err = windows.WSAIoctl(windows.Handle(fd), sioTCPInfo,
				(*byte)(unsafe.Pointer(&ver)), 4,
				(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)),
				&ret, nil, 0)
		})
		if cerr != nil || err != nil {
			return false, 0, false
		}
		return info.BytesInFlight > 0, info.BytesOut - uint64(info.BytesInFlight), true
	}
}
