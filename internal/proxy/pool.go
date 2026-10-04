package proxy

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Telegram closes an idle, un-initialised WS after ~90 s (measured), so
// pooled sockets are rotated well before that.
const (
	poolMaxAge       = 55 * time.Second
	cfwPoolMaxAge    = 85 * time.Second
	poolJanitorEvery = 3 * time.Second
	refillBackoffMin = 1 * time.Second
	refillBackoffMax = 60 * time.Second
)

type poolKey struct {
	dc    int
	media bool
}

// pooledWS sits idle with a background read pending, so a server close or
// reset is noticed the moment it happens instead of when a client gets it.
type pooledWS struct {
	ws      *WSConn
	created time.Time
	monDone chan error
	dead    atomic.Bool
	extra   string // cf worker domain
}

func newPooled(ws *WSConn, onDead func(*pooledWS)) *pooledWS {
	p := &pooledWS{ws: ws, created: time.Now(), monDone: make(chan error, 1)}
	go func() {
		_, err := ws.br.Peek(1)
		if err == nil {
			err = errors.New("unexpected data on idle socket")
		}
		if !isTimeout(err) {
			p.dead.Store(true)
		}
		p.monDone <- err
		if p.dead.Load() {
			onDead(p)
		}
	}()
	return p
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// detach stops the monitor and returns true if the socket is still healthy.
func (p *pooledWS) detach() bool {
	if p.dead.Load() {
		return false
	}
	p.ws.c.SetReadDeadline(time.Unix(1, 0))
	err := <-p.monDone
	if p.dead.Load() || !isTimeout(err) {
		p.ws.c.Close()
		return false
	}
	p.ws.c.SetReadDeadline(time.Time{})
	return true
}

type wsPool struct {
	srv       *Server
	mu        sync.Mutex
	idle      map[poolKey][]*pooledWS
	refilling map[poolKey]bool
	failures  map[poolKey]int
	retryAt   map[poolKey]time.Time
	ctx       context.Context
}

func newWSPool(s *Server, ctx context.Context) *wsPool {
	p := &wsPool{
		srv: s, ctx: ctx,
		idle:      map[poolKey][]*pooledWS{},
		refilling: map[poolKey]bool{},
		failures:  map[poolKey]int{},
		retryAt:   map[poolKey]time.Time{},
	}
	go p.janitor()
	return p
}

func (p *wsPool) size() int { return p.srv.cfg.PoolSize }

func (p *wsPool) remove(k poolKey, x *pooledWS) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.idle[k]
	for i, e := range l {
		if e == x {
			p.idle[k] = append(l[:i:i], l[i+1:]...)
			return true
		}
	}
	return false
}

func (p *wsPool) add(k poolKey, ws *WSConn) bool {
	p.mu.Lock()
	if len(p.idle[k]) >= p.size() || p.ctx.Err() != nil {
		p.mu.Unlock()
		return false
	}
	var px *pooledWS
	px = newPooled(ws, func(d *pooledWS) {
		if p.remove(k, d) {
			Log.Debugf("WS pool DC%d%s: idle socket closed by server, dropped", k.dc, mtag(k.media))
			d.ws.c.Close()
			p.scheduleRefill(k)
		}
	})
	p.idle[k] = append(p.idle[k], px)
	p.mu.Unlock()
	return true
}

func (p *wsPool) get(k poolKey) *WSConn {
	for {
		p.mu.Lock()
		l := p.idle[k]
		if len(l) == 0 {
			p.mu.Unlock()
			p.srv.Stats.PoolMisses.Add(1)
			p.scheduleRefill(k)
			return nil
		}
		x := l[0]
		p.idle[k] = l[1:]
		p.mu.Unlock()
		if time.Since(x.created) > poolMaxAge || !x.detach() {
			x.ws.c.Close()
			continue
		}
		p.srv.Stats.PoolHits.Add(1)
		p.scheduleRefill(k)
		return x.ws
	}
}

func (p *wsPool) scheduleRefill(k poolKey) {
	if p.size() <= 0 || p.ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	if p.refilling[k] || time.Now().Before(p.retryAt[k]) || len(p.idle[k]) >= p.size() {
		p.mu.Unlock()
		return
	}
	p.refilling[k] = true
	need := p.size() - len(p.idle[k])
	p.mu.Unlock()

	go func() {
		defer func() {
			p.mu.Lock()
			p.refilling[k] = false
			p.mu.Unlock()
		}()
		target := p.srv.cfg.DCRedirects[k.dc]
		if target == "" {
			return
		}
		var wg sync.WaitGroup
		var ok atomic.Int32
		for i := 0; i < need; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ws, err := p.srv.dialDirectWS(p.ctx, k.dc, k.media, false, 8*time.Second)
				if err != nil {
					return
				}
				if p.add(k, ws) {
					ok.Add(1)
				} else {
					ws.Close()
				}
			}()
		}
		wg.Wait()
		p.mu.Lock()
		if ok.Load() > 0 {
			delete(p.failures, k)
			delete(p.retryAt, k)
			p.mu.Unlock()
			p.srv.directRecovered(target)
			Log.Debugf("WS pool DC%d%s refilled: %d ready", k.dc, mtag(k.media), len(p.idle[k]))
			return
		}
		f := p.failures[k] + 1
		p.failures[k] = f
		d := refillBackoffMin << min(f-1, 12)
		if d > refillBackoffMax {
			d = refillBackoffMax
		}
		p.retryAt[k] = time.Now().Add(d)
		p.mu.Unlock()
		Log.Infof("WS pool refill failed for DC%d%s, retry in %.0fs", k.dc, mtag(k.media), d.Seconds())
	}()
}

func (p *wsPool) janitor() {
	t := time.NewTicker(poolJanitorEvery)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			p.closeAll()
			return
		case <-t.C:
		}
		var expired []*pooledWS
		p.mu.Lock()
		for k, l := range p.idle {
			keep := l[:0]
			for _, x := range l {
				if time.Since(x.created) > poolMaxAge {
					expired = append(expired, x)
				} else {
					keep = append(keep, x)
				}
			}
			p.idle[k] = keep
		}
		p.mu.Unlock()
		for _, x := range expired {
			x.ws.Close()
		}
		// Every configured DC is kept topped up (and probed while failing,
		// with backoff), not only the ones that were used before.
		for dc := range p.srv.cfg.DCRedirects {
			p.scheduleRefill(poolKey{dc, false})
			p.scheduleRefill(poolKey{dc, true})
		}
	}
}

func (p *wsPool) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, l := range p.idle {
		for _, x := range l {
			x.ws.c.Close()
		}
		delete(p.idle, k)
	}
}

func (p *wsPool) warmup() {
	for dc := range p.srv.cfg.DCRedirects {
		for _, m := range []bool{false, true} {
			p.scheduleRefill(poolKey{dc, m})
		}
	}
	Log.Infof("WS pool warmup started for %d DC(s)", len(p.srv.cfg.DCRedirects))
}

func mtag(media bool) string {
	if media {
		return "m"
	}
	return ""
}

// cfWorkerPool keeps one idle connection per DC to the user's CF worker.
type cfWorkerPool struct {
	srv  *Server
	mu   sync.Mutex
	idle map[int][]*pooledWS
	busy map[int]bool
	ctx  context.Context
}

func (p *cfWorkerPool) get(dc int) (*WSConn, string) {
	for {
		p.mu.Lock()
		l := p.idle[dc]
		if len(l) == 0 {
			p.mu.Unlock()
			return nil, ""
		}
		x := l[0]
		p.idle[dc] = l[1:]
		p.mu.Unlock()
		if time.Since(x.created) > cfwPoolMaxAge || !x.detach() {
			x.ws.c.Close()
			continue
		}
		return x.ws, x.extra
	}
}

func (p *cfWorkerPool) refill(dc int, dst string) {
	if len(p.srv.cfg.CFWorkers) == 0 || p.srv.cfg.PoolSize <= 0 || p.ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	if p.busy[dc] || len(p.idle[dc]) >= 1 {
		p.mu.Unlock()
		return
	}
	p.busy[dc] = true
	p.mu.Unlock()
	go func() {
		defer func() { p.mu.Lock(); p.busy[dc] = false; p.mu.Unlock() }()
		for _, d := range shuffled(p.srv.cfg.CFWorkers) {
			ws, err := p.srv.dialCFWorker(p.ctx, d, dc, dst)
			if err != nil {
				continue
			}
			p.mu.Lock()
			var x *pooledWS
			x = newPooled(ws, func(dd *pooledWS) {
				p.mu.Lock()
				l := p.idle[dc]
				for i, e := range l {
					if e == dd {
						p.idle[dc] = append(l[:i:i], l[i+1:]...)
						break
					}
				}
				p.mu.Unlock()
				dd.ws.c.Close()
			})
			x.extra = d
			p.idle[dc] = append(p.idle[dc], x)
			p.mu.Unlock()
			return
		}
	}()
}
