package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Server struct {
	cfg     Config
	Stats   *Stats
	pool    *wsPool
	cfwPool *cfWorkerPool
	routes  *routeState
	bal     *balancer

	ln     net.Listener
	lnMu   sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	conns  sync.Map

	lastActive atomic.Int64 // unix nano of the last client activity (LowPower pool sleep)
}

// touch marks client activity: the pool stays warm while Telegram uses the proxy.
func (s *Server) touch() { s.lastActive.Store(time.Now().UnixNano()) }

// idleFor is how long no client has used the proxy.
func (s *Server) idleFor() time.Duration {
	if s.Stats.ConnActive.Load() > 0 { // Telegram holds a session: it is in use
		return 0
	}
	return time.Duration(time.Now().UnixNano() - s.lastActive.Load())
}

func NewServer(cfg Config) *Server {
	s := &Server{cfg: cfg, Stats: &Stats{}, routes: newRouteState(), bal: &balancer{}}
	return s
}

func (s *Server) Config() Config { return s.cfg }

// Start binds the port and serves in the background.
func (s *Server) Start() error {
	if len(s.cfg.Secret) != 16 {
		return errors.New("secret must be 16 bytes (32 hex chars)")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(s.cfg.Host, fmt.Sprint(s.cfg.Port)))
	if err != nil {
		return err
	}
	s.ln = ln
	s.touch() // just started: warm up as before
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.pool = newWSPool(s, s.ctx)
	s.cfwPool = &cfWorkerPool{srv: s, ctx: s.ctx, idle: map[int][]*pooledWS{}, busy: map[int]bool{}}

	if len(s.cfg.CFUserDomains) > 0 {
		s.bal.setDomains(s.cfg.CFUserDomains)
	} else {
		s.bal.setDomains(defaultCFDomains())
		if s.cfg.CFProxy {
			go s.refreshCFDomains()
		}
	}

	s.logBanner()
	s.pool.warmup()
	for dc, ip := range DCDefaultIPs {
		if _, direct := s.cfg.DCRedirects[dc]; !direct {
			s.cfwPool.refill(dc, ip)
		}
	}
	go s.statsLoop()
	s.wg.Add(1)
	go s.acceptLoop()
	return nil
}

func (s *Server) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.lnMu.Lock()
	s.ln.Close()
	s.lnMu.Unlock()
	s.conns.Range(func(k, _ any) bool {
		k.(net.Conn).Close()
		return true
	})
	s.wg.Wait()
}

func (s *Server) listener() net.Listener {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	return s.ln
}

// acceptLoop serves clients. If the listening socket dies (iOS reclaims it
// while the app is suspended, a network stack reset...), it is recreated.
func (s *Server) acceptLoop() {
	defer s.wg.Done()
	backoff := 5 * time.Millisecond
	fails := 0
	for {
		ln := s.listener()
		c, err := ln.Accept()
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			fails++
			if fails <= 3 {
				Log.Warnf("accept: %v", err)
			}
			time.Sleep(backoff)
			backoff = min(backoff*2, time.Second)
			if fails >= 3 && s.ctx.Err() == nil {
				s.lnMu.Lock()
				s.ln.Close()
				nl, lerr := net.Listen("tcp", net.JoinHostPort(s.cfg.Host, fmt.Sprint(s.cfg.Port)))
				if lerr == nil {
					s.ln = nl
					fails = 0
					Log.Warnf("Listening socket recreated on %s:%d", s.cfg.Host, s.cfg.Port)
				}
				s.lnMu.Unlock()
			}
			continue
		}
		fails = 0
		backoff = 5 * time.Millisecond
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.conns.Store(c, struct{}{})
			defer s.conns.Delete(c)
			s.handle(c)
		}()
	}
}

func (s *Server) refreshCFDomains() {
	for {
		if l := fetchCFDomains(s.ctx); len(l) >= 3 {
			s.bal.setDomains(l)
			Log.Infof("CF proxy domain pool updated from GitHub (%d domains)", len(l))
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

func (s *Server) statsLoop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			Log.Infof("stats: %s", s.Stats.Summary())
		}
	}
}

func (s *Server) logBanner() {
	Log.Infof("%s", strings.Repeat("=", 60))
	Log.Infof("  Telegram MTProto WS Bridge Proxy (Go)")
	Log.Infof("  Listening on   %s:%d", s.cfg.Host, s.cfg.Port)
	Log.Infof("  Secret:        %x", s.cfg.Secret)
	if s.cfg.FakeTLSDomain != "" {
		Log.Infof("  Fake TLS:      %s", s.cfg.FakeTLSDomain)
	}
	Log.Infof("  Target DC IPs:")
	for _, dc := range []int{1, 2, 3, 4, 5, 203} {
		if ip, ok := s.cfg.DCRedirects[dc]; ok {
			Log.Infof("    DC%d: %s", dc, ip)
		}
	}
	if s.cfg.CFProxy {
		Log.Infof("  CF proxy:      enabled")
	}
	if len(s.cfg.CFWorkers) > 0 {
		Log.Infof("  CF worker:     enabled (%s)", strings.Join(s.cfg.CFWorkers, ", "))
	}
	if s.cfg.StallTimeout > 0 {
		Log.Infof("  Stall guard:   %.0fs", s.cfg.StallTimeout.Seconds())
	}
	Log.Infof("%s", strings.Repeat("=", 60))
	Log.Infof("  Connect: %s", ProxyLink(s.cfg))
	Log.Infof("%s", strings.Repeat("=", 60))
}

func (s *Server) handle(c net.Conn) {
	s.touch()
	s.Stats.ConnTotal.Add(1)
	s.Stats.ConnActive.Add(1)
	defer func() { s.Stats.ConnActive.Add(-1); s.touch() }()
	defer c.Close()
	setSockOpts(c, s.cfg.BufferSize)
	label := c.RemoteAddr().String()

	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReaderSize(c, 64<<10)

	if s.cfg.ProxyProtocol {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		f := strings.Fields(line)
		if len(f) >= 6 && f[0] == "PROXY" {
			label = f[2] + ":" + f[4]
		}
	}

	first, err := br.Peek(1)
	if err != nil {
		return
	}
	cl := &clientSide{r: br, w: c, c: c}
	var hs [handshakeLen]byte
	masking := s.cfg.FakeTLSDomain

	switch {
	case first[0] == tlsRecordHandshake && masking != "":
		hdr, err := br.Peek(5)
		if err != nil {
			return
		}
		recLen := int(binary.BigEndian.Uint16(hdr[3:5]))
		hello := make([]byte, 5+recLen)
		if _, err := io.ReadFull(br, hello); err != nil {
			return
		}
		cr, sid, ok := verifyClientHello(hello, s.cfg.Secret)
		if !ok {
			Log.Debugf("[%s] Fake TLS verify failed -> masking", label)
			c.SetReadDeadline(time.Time{})
			s.proxyToMasking(c, br, hello, masking, label)
			return
		}
		if _, err := c.Write(buildServerHello(s.cfg.Secret, cr, sid)); err != nil {
			return
		}
		ft := &fakeTLSConn{br: br, c: c}
		cl = &clientSide{r: ft, w: ft, c: c}
		if _, err := io.ReadFull(ft, hs[:]); err != nil {
			return
		}
	case masking != "":
		Log.Debugf("[%s] non-TLS byte 0x%02X -> HTTP redirect", label, first[0])
		fmt.Fprintf(c, "HTTP/1.1 301 Moved Permanently\r\nLocation: https://%s/\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", masking)
		return
	default:
		if _, err := io.ReadFull(br, hs[:]); err != nil {
			return
		}
	}
	c.SetReadDeadline(time.Time{})

	h, ok := parseClientHandshake(hs[:], s.cfg.Secret)
	if !ok {
		s.Stats.ConnBad.Add(1)
		Log.Warnf("[%s] bad handshake (wrong secret or proto)", label)
		c.SetReadDeadline(time.Now().Add(30 * time.Second))
		io.Copy(io.Discard, cl.r)
		return
	}
	dc, media := h.DC, h.Media
	test := s.cfg.ForceTestDC || dc >= 10000
	if dc >= 10000 {
		Log.Infof("[%s] test DC%d -> DC%d", label, dc, dc-10000)
		dc -= 10000
	}
	dcIdx := int16(dc)
	if media {
		dcIdx = -dcIdx
	}
	relayInit := generateRelayInit(h.Tag, dcIdx)
	cctx := buildCryptoCtx(h.PrekeyIV, s.cfg.Secret, relayInit)

	fallbackIPs := DCDefaultIPs
	if test {
		fallbackIPs = DCTestIPs
	}
	q := &connReq{
		dc: dc, media: media, test: test, label: label,
		dcKey:    fmt.Sprintf("%d%s%s", dc, map[bool]string{true: "t"}[test], mtag(media)),
		fallback: fallbackIPs[dc],
	}
	Log.Debugf("[%s] handshake ok: DC%d%s proto=0x%08X", label, dc, mtag(media), uint32(h.Tag))

	t0 := time.Now()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	up, err := s.connect(ctx, q)
	cancel()
	if err != nil {
		Log.Warnf("[%s] DC%d%s no route to Telegram: %v", label, dc, mtag(media), err)
		return
	}
	Log.Debugf("[%s] DC%d%s upstream %s ready in %dms", label, dc, mtag(media), up.Kind(), time.Since(t0).Milliseconds())

	var split *packetSplitter
	switch up.Kind() {
	case "ws", "cf":
		split = newPacketSplitter(h.Tag)
	}
	if err := up.Send([][]byte{relayInit}); err != nil {
		up.Close()
		Log.Warnf("[%s] DC%d%s sending init failed: %v", label, dc, mtag(media), err)
		return
	}
	s.bridge(cl, up, cctx, split, label, fmt.Sprintf("DC%d%s", dc, mtag(media)))
}
