package proxy

import (
	"context"
	"testing"
	"time"
)

func TestLowPowerPoolSizing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LowPower = true
	s := NewServer(cfg)
	s.touch()
	p := &wsPool{srv: s, ctx: context.Background()}

	if got := p.sizeFor(poolKey{2, false}); got != lowPowerPoolSize {
		t.Fatalf("regular pool: %d", got)
	}
	if got := p.sizeFor(poolKey{2, true}); got != 0 {
		t.Fatalf("media pool must be cold until media is used: %d", got)
	}
	p.mediaAt.Store(time.Now().UnixNano())
	if got := p.sizeFor(poolKey{2, true}); got != 1 {
		t.Fatalf("media pool after use: %d", got)
	}

	// 4 минуты без клиентов — пул спит
	s.lastActive.Store(time.Now().Add(-4 * time.Minute).UnixNano())
	if !p.asleep() || p.sizeFor(poolKey{2, false}) != 0 {
		t.Fatal("pool should sleep when idle")
	}
	// открытая сессия Telegram = активность
	s.Stats.ConnActive.Add(1)
	if p.asleep() {
		t.Fatal("active session must keep the pool awake")
	}
	s.Stats.ConnActive.Add(-1)

	// без LowPower всё как раньше
	cfg.LowPower = false
	s2 := NewServer(cfg)
	p2 := &wsPool{srv: s2, ctx: context.Background()}
	if p2.sizeFor(poolKey{2, true}) != cfg.PoolSize || p2.asleep() {
		t.Fatal("default mode changed")
	}
}
