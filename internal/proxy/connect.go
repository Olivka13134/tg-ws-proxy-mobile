package proxy

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errAllRedirect = errors.New("all WS domains answered with redirect")
	errNoRoute     = errors.New("no route available")
)

// frontingFirst mirrors the Python pool: once fronting (SNI spoofing) is
// needed it is tried first, once plain SNI works plain goes first.
var frontingFirst atomic.Bool

func init() { frontingFirst.Store(true) }

const frontSNI = "sprinthost.ru"

// dialDirectWS opens a WS to Telegram through the configured redirect IP,
// trying the DC's domains in order (redirects move on to the next).
func (s *Server) dialDirectWS(ctx context.Context, dc int, media, test bool, timeout time.Duration) (*WSConn, error) {
	target := s.cfg.DCRedirects[dc]
	if target == "" {
		return nil, errNoRoute
	}
	path := wsPath
	if test {
		path = wsPathTest
	}
	o := wsDialOpts{Addr: target, Path: path, Secure: true, Timeout: timeout, BufSize: s.cfg.BufferSize}
	anyRedirect, allRedirect := false, true
	var lastErr error
	for _, dom := range wsDomains(dc, media) {
		o.Host = dom
		try := func(sni string) (*WSConn, error) {
			o.SNI = sni
			return dialWS(ctx, o)
		}
		order := []string{"", frontSNI}
		if frontingFirst.Load() {
			order = []string{frontSNI, ""}
		}
		var err error
		for _, sni := range order {
			var ws *WSConn
			if ws, err = try(sni); err == nil {
				if sni == frontSNI {
					s.Stats.connFronting.Add(1)
				}
				frontingFirst.Store(sni == frontSNI)
				return ws, nil
			}
			var tce *tcpConnectError
			var he *WSHandshakeError
			if errors.As(err, &tce) || errors.As(err, &he) || ctx.Err() != nil {
				break // SNI is irrelevant for these
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
		var he *WSHandshakeError
		if errors.As(err, &he) {
			if he.IsRedirect() {
				anyRedirect = true
			} else {
				allRedirect = false
			}
			continue
		}
		allRedirect = false
		var tce *tcpConnectError
		if errors.As(err, &tce) && isTimeout(err) {
			s.noteDirectTimeout(target)
		}
		// network-level failure: the other domain shares the IP, don't
		// burn another timeout on it.
		break
	}
	if anyRedirect && allRedirect {
		return nil, errAllRedirect
	}
	if lastErr == nil {
		lastErr = errNoRoute
	}
	return nil, lastErr
}

func (s *Server) dialCFProxy(ctx context.Context, base string, dc int) (*WSConn, error) {
	dom := fmt.Sprintf("kws%d.%s", dc, base)
	return dialWS(ctx, wsDialOpts{Addr: dom, Host: dom, Secure: !s.cfg.NoSecure,
		Timeout: 10 * time.Second, BufSize: s.cfg.BufferSize})
}

func (s *Server) dialCFWorker(ctx context.Context, worker string, dc int, dst string) (*WSConn, error) {
	q := url.Values{"dst": {dst}, "dc": {fmt.Sprint(dc)}}
	return dialWS(ctx, wsDialOpts{Addr: worker, Host: worker, Path: "/apiws?" + q.Encode(),
		Secure: !s.cfg.NoSecure, Timeout: 10 * time.Second, BufSize: s.cfg.BufferSize})
}

func shuffled(l []string) []string {
	o := append([]string(nil), l...)
	rand.Shuffle(len(o), func(i, j int) { o[i], o[j] = o[j], o[i] })
	return o
}

// --- racing -------------------------------------------------------------

type candidate struct {
	name     string
	detached bool          // keeps running if another candidate wins (result recycled)
	after    time.Duration // start this long after the previous one if it hasn't finished
	run      func(ctx context.Context) (upstream, error)
}

// race runs candidates "happy eyeballs" style: each one starts when the
// previous one fails or after its delay, whichever comes first. The first
// success wins; late winners are recycled or closed.
func race(parent context.Context, cands []candidate) (upstream, string, error) {
	if len(cands) == 0 {
		return nil, "", errNoRoute
	}
	ctx, cancel := context.WithCancel(parent)
	type res struct {
		u    upstream
		name string
		err  error
	}
	results := make(chan res, len(cands))
	next, running := 0, 0
	start := func() {
		c := cands[next]
		next++
		running++
		rctx := ctx
		if c.detached {
			rctx = context.WithoutCancel(parent)
		}
		go func() {
			u, err := c.run(rctx)
			results <- res{u, c.name, err}
		}()
	}
	start()
	var errs []string
	var timer *time.Timer
	resetTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		if next < len(cands) {
			timer = time.NewTimer(cands[next].after)
		}
	}
	resetTimer()
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		var tc <-chan time.Time
		if timer != nil {
			tc = timer.C
		}
		select {
		case r := <-results:
			running--
			if r.err == nil {
				cancel()
				left := running
				if left > 0 {
					go func() {
						for i := 0; i < left; i++ {
							if lr := <-results; lr.err == nil {
								discard(lr.u)
							}
						}
					}()
				}
				return r.u, r.name, nil
			}
			if !errors.Is(r.err, context.Canceled) {
				errs = append(errs, r.name+": "+errText(r.err))
			}
			if next < len(cands) {
				start()
				resetTimer()
			} else if running == 0 {
				cancel()
				return nil, "", fmt.Errorf("%s", strings.Join(errs, "; "))
			}
		case <-tc:
			timer = nil
			start()
			resetTimer()
		case <-parent.Done():
			cancel()
			left := running
			go func() {
				for i := 0; i < left; i++ {
					if lr := <-results; lr.err == nil {
						discard(lr.u)
					}
				}
			}()
			return nil, "", parent.Err()
		}
	}
}

// --- route selection ----------------------------------------------------

type routeState struct {
	mu         sync.Mutex
	blacklist  map[string]time.Time // dcKey -> until (all 302)
	dcFail     map[string]time.Time // dcKey -> until (short connect timeout)
	ipFail     map[string]time.Time // ip -> until (timed out)
	ipFailures map[string]int
}

func newRouteState() *routeState {
	return &routeState{
		blacklist: map[string]time.Time{}, dcFail: map[string]time.Time{},
		ipFail: map[string]time.Time{}, ipFailures: map[string]int{},
	}
}

func (s *Server) noteDirectTimeout(ip string) {
	r := s.routes
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Now().Before(r.ipFail[ip]) {
		return // already cooling down
	}
	f := r.ipFailures[ip] + 1
	r.ipFailures[ip] = f
	cd := min(10*time.Second<<min(f-1, 5), 5*time.Minute)
	r.ipFail[ip] = time.Now().Add(cd)
	Log.Warnf("Direct WS to %s timed out -> fallbacks go first for %ds (pool keeps probing)", ip, int(cd.Seconds()))
}

func (s *Server) directRecovered(ip string) {
	r := s.routes
	r.mu.Lock()
	if _, ok := r.ipFail[ip]; ok {
		Log.Infof("Direct WS to %s works again", ip)
	}
	delete(r.ipFail, ip)
	delete(r.ipFailures, ip)
	r.mu.Unlock()
}

type connReq struct {
	dc       int
	media    bool
	test     bool
	label    string
	dcKey    string
	fallback string // default DC IP for TCP / CF worker fallback
}

func (s *Server) directCandidate(q *connReq, poolOnly bool) candidate {
	k := poolKey{q.dc, q.media}
	recycle := func(ws *WSConn) bool {
		if q.test {
			return false
		}
		return s.pool.add(k, ws)
	}
	return candidate{name: "ws", detached: !poolOnly, run: func(ctx context.Context) (upstream, error) {
		if !q.test {
			if ws := s.pool.get(k); ws != nil {
				Log.Infof("[%s] DC%d%s -> pool hit via %s", q.label, q.dc, mtag(q.media), s.cfg.DCRedirects[q.dc])
				return &wsUpstream{ws: ws, kind: "ws", recycle: recycle}, nil
			}
		}
		if poolOnly {
			// IP is cooling down: the pool keeps probing in the background
			// and clears the cooldown as soon as it connects again.
			return nil, errNoRoute
		}
		r := s.routes
		r.mu.Lock()
		timeout := 5 * time.Second
		if time.Now().Before(r.dcFail[q.dcKey]) {
			timeout = 2 * time.Second
		}
		r.mu.Unlock()
		target := s.cfg.DCRedirects[q.dc]
		Log.Infof("[%s] DC%d%s -> wss://%s via %s", q.label, q.dc, mtag(q.media), wsDomains(q.dc, q.media)[0], target)
		ws, err := s.dialDirectWS(ctx, q.dc, q.media, q.test, timeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil, context.Canceled
			}
			s.Stats.WSErrors.Add(1)
			r.mu.Lock()
			switch {
			case errors.Is(err, errAllRedirect):
				r.blacklist[q.dcKey] = time.Now().Add(10 * time.Minute)
				Log.Warnf("[%s] DC%d%s WS blacklisted for 10 min (all redirects)", q.label, q.dc, mtag(q.media))
			case isTimeout(err) || errors.Is(err, context.DeadlineExceeded):
				// cooldown recorded by dialDirectWS
			default:
				r.dcFail[q.dcKey] = time.Now().Add(60 * time.Second)
				Log.Warnf("[%s] DC%d%s WS connect failed: %v", q.label, q.dc, mtag(q.media), err)
			}
			r.mu.Unlock()
			return nil, err
		}
		s.directRecovered(target)
		return &wsUpstream{ws: ws, kind: "ws", recycle: recycle}, nil
	}}
}

func (s *Server) fallbackCandidates(q *connReq) []candidate {
	var out []candidate
	if len(s.cfg.CFWorkers) > 0 && q.fallback != "" {
		out = append(out, candidate{name: "cfw", after: 1 * time.Second, run: func(ctx context.Context) (upstream, error) {
			if !q.test {
				if ws, dom := s.cfwPool.get(q.dc); ws != nil {
					Log.Infof("[%s] DC%d%s -> CF worker pool hit via %s", q.label, q.dc, mtag(q.media), dom)
					s.cfwPool.refill(q.dc, q.fallback)
					return &wsUpstream{ws: ws, kind: "cfw"}, nil
				}
			}
			u, err := staggered(ctx, shuffled(s.cfg.CFWorkers), 400*time.Millisecond, func(ctx context.Context, d string) (*WSConn, error) {
				Log.Infof("[%s] DC%d%s -> trying CF worker %s", q.label, q.dc, mtag(q.media), d)
				return s.dialCFWorker(ctx, d, q.dc, q.fallback)
			})
			if err != nil {
				return nil, err
			}
			s.cfwPool.refill(q.dc, q.fallback)
			return &wsUpstream{ws: u, kind: "cfw"}, nil
		}})
	}
	if s.cfg.CFProxy && !q.test {
		out = append(out, candidate{name: "cf", after: 1 * time.Second, run: func(ctx context.Context) (upstream, error) {
			doms := s.bal.order(q.dc)
			if len(doms) == 0 {
				return nil, errNoRoute
			}
			Log.Infof("[%s] DC%d%s -> trying CF proxy", q.label, q.dc, mtag(q.media))
			var won atomic.Value
			ws, err := staggered(ctx, doms, 400*time.Millisecond, func(ctx context.Context, d string) (*WSConn, error) {
				w, err := s.dialCFProxy(ctx, d, q.dc)
				if err == nil {
					won.CompareAndSwap(nil, d)
				}
				return w, err
			})
			if err != nil {
				return nil, err
			}
			if d, _ := won.Load().(string); d != "" && s.bal.markGood(q.dc, d) {
				Log.Infof("[%s] Switched active CF domain for DC%d", q.label, q.dc)
			}
			return &wsUpstream{ws: ws, kind: "cf"}, nil
		}})
	}
	if q.fallback != "" {
		out = append(out, candidate{name: "tcp", after: 1 * time.Second, run: func(ctx context.Context) (upstream, error) {
			Log.Infof("[%s] DC%d%s -> TCP fallback to %s:443", q.label, q.dc, mtag(q.media), q.fallback)
			d := net.Dialer{Timeout: 10 * time.Second}
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(q.fallback, "443"))
			if err != nil {
				return nil, err
			}
			setSockOpts(c, s.cfg.BufferSize)
			return &tcpUpstream{c: c}, nil
		}})
	}
	return out
}

// staggered tries items with a small head start each; first success wins.
// At most 4 attempts are in flight at once.
func staggered(ctx context.Context, items []string, step time.Duration, dial func(context.Context, string) (*WSConn, error)) (*WSConn, error) {
	cands := make([]candidate, 0, len(items))
	for _, it := range items {
		it := it
		cands = append(cands, candidate{name: it, after: step, run: func(ctx context.Context) (upstream, error) {
			ws, err := dial(ctx, it)
			if err != nil {
				return nil, err
			}
			return &wsUpstream{ws: ws}, nil
		}})
	}
	// cap concurrency by grouping into windows of 4
	const window = 4
	var lastErr error
	for i := 0; i < len(cands); i += window {
		j := min(i+window, len(cands))
		u, _, err := race(ctx, cands[i:j])
		if err == nil {
			return u.(*wsUpstream).ws, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = errNoRoute
	}
	return nil, lastErr
}

// connect picks the fastest working route to Telegram for this client.
func (s *Server) connect(ctx context.Context, q *connReq) (upstream, error) {
	r := s.routes
	now := time.Now()
	target, inCfg := s.cfg.DCRedirects[q.dc]
	r.mu.Lock()
	black := now.Before(r.blacklist[q.dcKey])
	ipCool := inCfg && now.Before(r.ipFail[target])
	r.mu.Unlock()

	fb := s.fallbackCandidates(q)
	var cands []candidate
	switch {
	case !inCfg:
		Log.Infof("[%s] DC%d not in config -> fallback", q.label, q.dc)
		cands = fb
	case black:
		Log.Infof("[%s] DC%d%s WS blacklisted -> fallback", q.label, q.dc, mtag(q.media))
		cands = fb
	case ipCool && len(fb) > 0:
		// Recently timed out: a pool hit is still used (instant), otherwise
		// fallbacks start immediately.
		cands = append([]candidate{s.directCandidate(q, true)}, fb...)
	default:
		cands = append([]candidate{s.directCandidate(q, false)}, fb...)
		if len(fb) > 0 {
			cands[1].after = s.cfg.FallbackHead
		}
	}
	u, name, err := race(ctx, cands)
	if err != nil {
		return nil, err
	}
	if name != "ws" && inCfg && !black && !ipCool {
		// A fallback beat the direct route's head start: treat the direct IP
		// as slow right away. If its dial still succeeds in the background,
		// directRecovered() lifts this immediately.
		s.noteDirectTimeout(target)
	}
	switch name {
	case "ws":
		s.Stats.ConnWS.Add(1)
	case "cf":
		s.Stats.ConnCF.Add(1)
	case "cfw":
		s.Stats.ConnCFWorker.Add(1)
	case "tcp":
		s.Stats.ConnTCP.Add(1)
	}
	return u, nil
}
