package proxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type upstream interface {
	Send(parts [][]byte) error
	Recv() ([]byte, error) // valid until the next Recv
	Buffered() bool        // a whole message can be read without blocking
	Close()
	Kind() string // "ws", "cf", "cfw", "tcp"
	Conn() net.Conn
}

type wsUpstream struct {
	ws      *WSConn
	kind    string
	recycle func(*WSConn) bool // return an unused conn to the pool
}

func (u *wsUpstream) Send(parts [][]byte) error { return u.ws.WriteMessages(parts...) }
func (u *wsUpstream) Recv() ([]byte, error)     { return u.ws.ReadMessage() }
func (u *wsUpstream) Buffered() bool            { return u.ws.NextFrameBuffered() }
func (u *wsUpstream) Close()                    { u.ws.Close() }
func (u *wsUpstream) Kind() string              { return u.kind }
func (u *wsUpstream) Conn() net.Conn            { return u.ws.c }

// discard is used for connections that lost a race and were never used.
func discard(u upstream) {
	if w, ok := u.(*wsUpstream); ok && w.recycle != nil && w.recycle(w.ws) {
		return
	}
	u.Close()
}

type tcpUpstream struct {
	c   net.Conn
	buf []byte
}

func (u *tcpUpstream) Send(parts [][]byte) error {
	if len(parts) == 1 {
		_, err := u.c.Write(parts[0])
		return err
	}
	b := net.Buffers(append([][]byte(nil), parts...))
	_, err := b.WriteTo(u.c)
	return err
}
func (u *tcpUpstream) Recv() ([]byte, error) {
	if u.buf == nil {
		u.buf = make([]byte, 64<<10)
	}
	n, err := u.c.Read(u.buf)
	if n > 0 {
		return u.buf[:n], nil
	}
	if err == nil {
		err = io.ErrNoProgress
	}
	return nil, err
}
func (u *tcpUpstream) Buffered() bool { return false }
func (u *tcpUpstream) Close()         { u.c.Close() }
func (u *tcpUpstream) Kind() string   { return "tcp" }
func (u *tcpUpstream) Conn() net.Conn { return u.c }

// clientSide is the Telegram app's connection (plain or inside fake TLS).
type clientSide struct {
	r io.Reader
	w io.Writer
	c net.Conn
}

// bridge pumps both directions with re-encryption.
//
// Watchdog: if bytes we sent to Telegram stay unacknowledged at the TCP
// level for StallTimeout (silent drop, DPI freeze), the path is dead. We
// close the client connection right away so the Telegram app reconnects at
// once to a fresh pre-warmed upstream, instead of sitting on a dead socket
// while messages pile up. TCP-level on purpose: MTProto acks get no reply,
// so "no answer" would misfire, but the server's TCP always acks.
func (s *Server) bridge(cl *clientSide, up upstream, ctx *cryptoCtx, split *packetSplitter, label, dcTag string) {
	var (
		upBytes, downBytes, upPkts, downPkts atomic.Int64
		once                                 sync.Once
		reason                               = "normal"
		reasonMu                             sync.Mutex
	)
	start := time.Now()
	finished := make(chan struct{})
	finish := func(r string) {
		once.Do(func() {
			reasonMu.Lock()
			reason = r
			reasonMu.Unlock()
			close(finished)
			cl.c.Close()
			up.Close()
		})
	}

	// client -> telegram
	go func() {
		buf := make([]byte, 64<<10)
		var parts [][]byte
		single := make([][]byte, 1)
		for {
			n, err := cl.r.Read(buf)
			if n > 0 {
				p := buf[:n]
				ctx.cltDec.XORKeyStream(p, p)
				if split != nil {
					split.feed(p)
				}
				ctx.tgEnc.XORKeyStream(p, p)
				if split != nil {
					parts = split.cut(p, parts)
				} else {
					single[0] = p
					parts = single
				}
				if len(parts) > 0 {
					if serr := up.Send(parts); serr != nil {
						finish("upstream: " + errText(serr))
						return
					}
				}
				upBytes.Add(int64(n))
				upPkts.Add(1)
				s.Stats.BytesUp.Add(int64(n))
			}
			if err != nil {
				if split != nil {
					if tail := split.flush(); len(tail) > 0 {
						_ = up.Send([][]byte{tail})
					}
				}
				if errors.Is(err, io.EOF) {
					finish("normal")
				} else {
					finish("client: " + errText(err))
				}
				return
			}
		}
	}()

	// telegram -> client
	go func() {
		out := make([]byte, 0, 256<<10)
		for {
			msg, err := up.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					finish("upstream: closed")
				} else {
					finish("upstream: " + errText(err))
				}
				return
			}
			ctx.tgDec.XORKeyStream(msg, msg)
			ctx.cltEnc.XORKeyStream(msg, msg)
			total := len(msg)
			pk := int64(1)
			data := msg
			if up.Buffered() {
				// Coalesce only what has already arrived: no added latency.
				out = append(out[:0], msg...)
				for up.Buffered() && len(out) < 1<<20 {
					m2, err2 := up.Recv()
					if err2 != nil {
						break
					}
					ctx.tgDec.XORKeyStream(m2, m2)
					ctx.cltEnc.XORKeyStream(m2, m2)
					out = append(out, m2...)
					total += len(m2)
					pk++
				}
				data = out
			}
			if _, werr := cl.w.Write(data); werr != nil {
				finish("client: " + errText(werr))
				return
			}
			downBytes.Add(int64(total))
			downPkts.Add(pk)
			s.Stats.BytesDown.Add(int64(total))
			if cap(out) > 4<<20 {
				out = make([]byte, 0, 256<<10)
			}
		}
	}()

	if st := s.cfg.StallTimeout; st > 0 {
		if probe := probeFor(up.Conn()); probe != nil {
			go func() {
				// Poll fast only while data is in flight; an idle session is
				// checked rarely (fewer wake-ups on phones and laptops).
				const busyEvery = 250 * time.Millisecond
				idleEvery := 1 * time.Second
				if s.cfg.LowPower {
					idleEvery = 2 * time.Second
				}
				t := time.NewTimer(busyEvery)
				defer t.Stop()
				var lastAcked uint64
				var stuckSince time.Time
				next := busyEvery
				for {
					t.Reset(next)
					select {
					case <-finished:
						return
					case <-t.C:
					}
					next = busyEvery
					unacked, acked, ok := probe()
					if !ok {
						Log.Debugf("[%s] stall guard unavailable on this socket", label)
						return
					}

					// Only growth counts as progress (on macOS/iOS the counter
					// can dip when new, not-yet-sent data is buffered).
					if !unacked || acked > lastAcked {
						lastAcked = max(lastAcked, acked)
						stuckSince = time.Time{}
						if !unacked {
							next = idleEvery
						}
						continue
					}
					if stuckSince.IsZero() {
						stuckSince = time.Now()
					} else if time.Since(stuckSince) > st {
						s.Stats.Stalls.Add(1)
						finish(fmt.Sprintf("stall: Telegram stopped acknowledging data for %.0fs", st.Seconds()))
						return
					}
				}
			}()
		}
	}

	<-finished
	reasonMu.Lock()
	r := reason
	reasonMu.Unlock()
	if len(r) >= 5 && r[:5] == "stall" {
		Log.Warnf("[%s] %s %s session dropped (%s) after %.1fs -> client will reconnect",
			label, dcTag, up.Kind(), r, time.Since(start).Seconds())
	}
	Log.Infof("[%s] %s %s session closed (%s): ^%s (%d) v%s (%d) in %.1fs",
		label, dcTag, up.Kind(), r,
		HumanBytes(upBytes.Load()), upPkts.Load(),
		HumanBytes(downBytes.Load()), downPkts.Load(),
		time.Since(start).Seconds())
}

func errText(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, os.ErrDeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, net.ErrClosed) {
		return "closed"
	}
	return err.Error()
}
