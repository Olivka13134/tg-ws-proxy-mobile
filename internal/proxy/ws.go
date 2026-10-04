package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	opCont   = 0x0
	opText   = 0x1
	opBinary = 0x2
	opClose  = 0x8
	opPing   = 0x9
	opPong   = 0xA

	maxWSMessage = 16 << 20
)

type WSHandshakeError struct {
	Status   int
	Line     string
	Location string
}

// tcpConnectError marks failures before TLS started: SNI can't matter.
type tcpConnectError struct{ err error }

func (e *tcpConnectError) Error() string { return "tcp connect: " + e.err.Error() }
func (e *tcpConnectError) Unwrap() error { return e.err }

func (e *WSHandshakeError) Error() string { return "HTTP " + strconv.Itoa(e.Status) + ": " + e.Line }
func (e *WSHandshakeError) IsRedirect() bool {
	switch e.Status {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}

// WSConn is a minimal client-side WebSocket tuned for the proxy:
// zero-copy-ish frame reads, one syscall per batch of frames on write.
type WSConn struct {
	c      net.Conn
	br     *bufio.Reader
	wmu    sync.Mutex
	wbuf   []byte
	rbuf   []byte
	frag   []byte
	closed atomic.Bool
}

type wsDialOpts struct {
	Addr    string // ip or host to TCP-connect to
	Host    string // HTTP Host header
	SNI     string // TLS SNI (defaults to Host)
	Path    string
	Secure  bool
	Timeout time.Duration
	BufSize int
}

var systemRoots *x509.CertPool

func init() {
	systemRoots, _ = x509.SystemCertPool()
}

func setSockOpts(c net.Conn, bufSize int) {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tc.SetNoDelay(true)
	if bufSize > 0 {
		_ = tc.SetReadBuffer(bufSize)
		_ = tc.SetWriteBuffer(bufSize)
	}
	_ = tc.SetKeepAlive(true)
	_ = tc.SetKeepAlivePeriod(15 * time.Second)
}

func dialWS(ctx context.Context, o wsDialOpts) (*WSConn, error) {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Path == "" {
		o.Path = wsPath
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	port := "443"
	if !o.Secure {
		port = "80"
	}
	d := net.Dialer{}
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(o.Addr, port))
	if err != nil {
		return nil, &tcpConnectError{err}
	}
	setSockOpts(raw, o.BufSize)

	var conn net.Conn = raw
	if o.Secure {
		sni := o.SNI
		if sni == "" {
			sni = o.Host
		}
		cfg := &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS12}
		if sni != o.Host {
			// Domain fronting: SNI differs from Host. Verify the chain but
			// not the hostname, same as the Python version.
			cfg.InsecureSkipVerify = true
			cfg.VerifyConnection = func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("no peer certificate")
				}
				inter := x509.NewCertPool()
				for _, c := range cs.PeerCertificates[1:] {
					inter.AddCert(c)
				}
				_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
					Roots: systemRoots, Intermediates: inter,
				})
				return err
			}
		}
		tc := tls.Client(raw, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		conn = tc
	}

	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	key := base64.StdEncoding.EncodeToString(randomBytes(16))
	req := "GET " + o.Path + " HTTP/1.1\r\n" +
		"Host: " + o.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Protocol: binary\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReaderSize(conn, 128<<10)
	var lines []string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			if len(lines) == 0 && errors.Is(err, io.EOF) {
				return nil, &WSHandshakeError{Status: 0, Line: "empty response"}
			}
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		lines = append(lines, line)
		if len(lines) > 100 {
			conn.Close()
			return nil, errors.New("ws handshake: too many headers")
		}
	}
	conn.SetDeadline(time.Time{})
	if len(lines) == 0 {
		conn.Close()
		return nil, &WSHandshakeError{Line: "empty response"}
	}
	parts := strings.SplitN(lines[0], " ", 3)
	status := 0
	if len(parts) >= 2 {
		status, _ = strconv.Atoi(parts[1])
	}
	if status == 101 {
		return &WSConn{c: conn, br: br}, nil
	}
	he := &WSHandshakeError{Status: status, Line: lines[0]}
	for _, h := range lines[1:] {
		if k, v, ok := strings.Cut(h, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "location") {
			he.Location = strings.TrimSpace(v)
		}
	}
	conn.Close()
	return nil, he
}

func maskXOR(dst, src []byte, key [4]byte) {
	k8 := uint64(binary.LittleEndian.Uint32(key[:]))
	k8 |= k8 << 32
	i := 0
	for ; i+8 <= len(src); i += 8 {
		binary.LittleEndian.PutUint64(dst[i:], binary.LittleEndian.Uint64(src[i:])^k8)
	}
	for ; i < len(src); i++ {
		dst[i] = src[i] ^ key[i&3]
	}
}

func appendFrame(buf []byte, op byte, payload []byte) []byte {
	var hdr [14]byte
	hdr[0] = 0x80 | op
	n := len(payload)
	h := 2
	switch {
	case n < 126:
		hdr[1] = 0x80 | byte(n)
	case n < 65536:
		hdr[1] = 0x80 | 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
		h = 4
	default:
		hdr[1] = 0x80 | 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
		h = 10
	}
	var key [4]byte
	fastRand(key[:])
	copy(hdr[h:], key[:])
	h += 4
	buf = append(buf, hdr[:h]...)
	start := len(buf)
	buf = append(buf, payload...)
	maskXOR(buf[start:], buf[start:], key)
	return buf
}

// WriteMessages sends each part as its own binary WS message, in one write.
func (w *WSConn) WriteMessages(parts ...[]byte) error {
	if w.closed.Load() {
		return net.ErrClosed
	}
	w.wmu.Lock()
	defer w.wmu.Unlock()
	buf := w.wbuf[:0]
	for _, p := range parts {
		buf = appendFrame(buf, opBinary, p)
	}
	_, err := w.c.Write(buf)
	if cap(buf) <= 1<<20 {
		w.wbuf = buf[:0]
	} else {
		w.wbuf = nil
	}
	return err
}

func (w *WSConn) writeControl(op byte, payload []byte) error {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	w.c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := w.c.Write(appendFrame(nil, op, payload))
	w.c.SetWriteDeadline(time.Time{})
	return err
}

// ReadMessage returns the next data message. The returned slice is only
// valid until the next call. io.EOF on a close frame.
func (w *WSConn) ReadMessage() ([]byte, error) {
	w.frag = w.frag[:0]
	fragmented := false
	for {
		var h [2]byte
		if _, err := io.ReadFull(w.br, h[:]); err != nil {
			return nil, err
		}
		fin := h[0]&0x80 != 0
		op := h[0] & 0x0F
		n := uint64(h[1] & 0x7F)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(w.br, b[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(w.br, b[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		if n > maxWSMessage {
			return nil, fmt.Errorf("ws frame too large: %d", n)
		}
		var mkey [4]byte
		masked := h[1]&0x80 != 0
		if masked {
			if _, err := io.ReadFull(w.br, mkey[:]); err != nil {
				return nil, err
			}
		}
		if cap(w.rbuf) < int(n) {
			w.rbuf = make([]byte, n, max(int(n), 64<<10))
		}
		p := w.rbuf[:n]
		if _, err := io.ReadFull(w.br, p); err != nil {
			return nil, err
		}
		if masked {
			maskXOR(p, p, mkey)
		}
		switch op {
		case opClose:
			w.closed.Store(true)
			var code []byte
			if len(p) >= 2 {
				code = p[:2]
			}
			_ = w.writeControl(opClose, code)
			return nil, io.EOF
		case opPing:
			_ = w.writeControl(opPong, append([]byte(nil), p...))
			continue
		case opPong:
			continue
		case opCont, opText, opBinary:
			if fin && !fragmented {
				return p, nil
			}
			fragmented = true
			w.frag = append(w.frag, p...)
			if len(w.frag) > maxWSMessage {
				return nil, errors.New("ws message too large")
			}
			if fin {
				return w.frag, nil
			}
		}
	}
}

// NextFrameBuffered reports whether a complete frame is already sitting in
// the read buffer, so the caller can coalesce without blocking.
func (w *WSConn) NextFrameBuffered() bool {
	b := w.br.Buffered()
	if b < 2 {
		return false
	}
	h, _ := w.br.Peek(min(b, 14))
	n := uint64(h[1] & 0x7F)
	hl := 2
	switch n {
	case 126:
		if len(h) < 4 {
			return false
		}
		n = uint64(binary.BigEndian.Uint16(h[2:]))
		hl = 4
	case 127:
		if len(h) < 10 {
			return false
		}
		n = binary.BigEndian.Uint64(h[2:])
		hl = 10
	}
	if h[1]&0x80 != 0 {
		hl += 4
	}
	return uint64(b) >= uint64(hl)+n
}

func (w *WSConn) Close() error {
	if w.closed.Swap(true) {
		return w.c.Close()
	}
	_ = w.writeControl(opClose, nil)
	return w.c.Close()
}
