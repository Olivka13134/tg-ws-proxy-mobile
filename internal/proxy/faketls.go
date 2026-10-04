package proxy

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"time"
)

const (
	tlsRecordCCS       = 0x14
	tlsRecordHandshake = 0x16
	tlsRecordAppData   = 0x17
	tlsAppDataMax      = 16384
	tlsTimeTolerance   = 120
)

var serverHelloTemplate = func() []byte {
	b := []byte{0x16, 0x03, 0x03, 0x00, 0x7a, 0x02, 0x00, 0x00, 0x76, 0x03, 0x03}
	b = append(b, make([]byte, 32)...)
	b = append(b, 0x20)
	b = append(b, make([]byte, 32)...)
	b = append(b, 0x13, 0x01, 0x00, 0x00, 0x2e, 0x00, 0x33, 0x00, 0x24, 0x00, 0x1d, 0x00, 0x20)
	b = append(b, make([]byte, 32)...)
	b = append(b, 0x00, 0x2b, 0x00, 0x02, 0x03, 0x04)
	return b
}()

func verifyClientHello(data, secret []byte) (clientRandom, sessionID []byte, ok bool) {
	if len(data) < 43 || data[0] != tlsRecordHandshake || data[5] != 0x01 {
		return nil, nil, false
	}
	clientRandom = append([]byte(nil), data[11:43]...)
	z := append([]byte(nil), data...)
	for i := 11; i < 43; i++ {
		z[i] = 0
	}
	m := hmac.New(sha256.New, secret)
	m.Write(z)
	exp := m.Sum(nil)
	if !hmac.Equal(exp[:28], clientRandom[:28]) {
		return nil, nil, false
	}
	var ts [4]byte
	for i := 0; i < 4; i++ {
		ts[i] = clientRandom[28+i] ^ exp[28+i]
	}
	t := int64(binary.LittleEndian.Uint32(ts[:]))
	if d := time.Now().Unix() - t; d > tlsTimeTolerance || d < -tlsTimeTolerance {
		return nil, nil, false
	}
	sessionID = make([]byte, 32)
	if len(data) >= 76 && data[43] == 0x20 {
		copy(sessionID, data[44:76])
	}
	return clientRandom, sessionID, true
}

func buildServerHello(secret, clientRandom, sessionID []byte) []byte {
	sh := append([]byte(nil), serverHelloTemplate...)
	copy(sh[44:76], sessionID)
	copy(sh[89:121], randomBytes(32))
	sh = append(sh, 0x14, 0x03, 0x03, 0x00, 0x01, 0x01)
	n := 1900 + rand.IntN(201)
	sh = append(sh, 0x17, 0x03, 0x03, byte(n>>8), byte(n))
	sh = append(sh, randomBytes(n)...)
	m := hmac.New(sha256.New, secret)
	m.Write(clientRandom)
	m.Write(sh)
	copy(sh[11:43], m.Sum(nil))
	return sh
}

// fakeTLSConn unwraps/wraps TLS application-data records.
type fakeTLSConn struct {
	br   *bufio.Reader
	c    net.Conn
	left int
	wbuf []byte
}

func (f *fakeTLSConn) Read(p []byte) (int, error) {
	for f.left == 0 {
		var h [5]byte
		if _, err := io.ReadFull(f.br, h[:]); err != nil {
			return 0, err
		}
		n := int(binary.BigEndian.Uint16(h[3:]))
		switch h[0] {
		case tlsRecordCCS:
			if _, err := f.br.Discard(n); err != nil {
				return 0, err
			}
			continue
		case tlsRecordAppData:
			f.left = n
		default:
			return 0, io.EOF
		}
	}
	if len(p) > f.left {
		p = p[:f.left]
	}
	n, err := f.br.Read(p)
	f.left -= n
	return n, err
}

func (f *fakeTLSConn) Write(p []byte) (int, error) {
	b := f.wbuf[:0]
	for off := 0; off < len(p); off += tlsAppDataMax {
		chunk := p[off:min(off+tlsAppDataMax, len(p))]
		b = append(b, 0x17, 0x03, 0x03, byte(len(chunk)>>8), byte(len(chunk)))
		b = append(b, chunk...)
	}
	_, err := f.c.Write(b)
	if cap(b) <= 1<<20 {
		f.wbuf = b[:0]
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// proxyToMasking forwards a non-proxy client to the real masking site, so
// probing the port looks like an ordinary HTTPS server.
func (s *Server) proxyToMasking(cl net.Conn, br *bufio.Reader, initial []byte, domain, label string) {
	up, err := net.DialTimeout("tcp", net.JoinHostPort(domain, "443"), 10*time.Second)
	if err != nil {
		Log.Warnf("[%s] masking: cannot connect to %s:443: %v", label, domain, err)
		return
	}
	defer up.Close()
	s.Stats.ConnMasked.Add(1)
	if _, err := up.Write(initial); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, br); up.(*net.TCPConn).CloseWrite(); done <- struct{}{} }()
	go func() { io.Copy(cl, up); cl.Close(); done <- struct{}{} }()
	<-done
	<-done
}

var errBadInit = errors.New("bad init")
