package proxy

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Config struct {
	Host          string
	Port          int
	Secret        []byte // 16 bytes
	DCRedirects   map[int]string
	BufferSize    int
	PoolSize      int
	CFProxy       bool     // fallback via Cloudflare-proxied domains
	CFUserDomains []string // if set, replaces the built-in list
	CFWorkers     []string
	NoSecure      bool
	FakeTLSDomain string
	ProxyProtocol bool
	ForceTestDC   bool

	// New knobs (not in the Python version)
	StallTimeout time.Duration // 0 disables the dead-upstream watchdog
	FallbackHead time.Duration // how long direct WS gets before fallbacks start in parallel

	// LowPower is the phone/laptop mode: the warm pool is small, sleeps when
	// Telegram has not used the proxy for a few minutes, and timers tick
	// less often. Pre-warmed sockets are rotated every ~55 s, so a full pool
	// alone costs ~1000 TLS handshakes an hour and keeps the radio awake.
	LowPower bool
}

func DefaultConfig() Config {
	return Config{
		Host:         "127.0.0.1",
		Port:         1443,
		Secret:       randomBytes(16),
		DCRedirects:  map[int]string{2: "149.154.167.220", 4: "149.154.167.220"},
		BufferSize:   256 << 10,
		PoolSize:     4,
		CFProxy:      true,
		StallTimeout: 5 * time.Second,
		FallbackHead: 1000 * time.Millisecond,
	}
}

func ParseDCIPList(list []string) (map[int]string, error) {
	m := map[int]string{}
	for _, e := range list {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		dcS, ip, ok := strings.Cut(e, ":")
		if !ok {
			return nil, fmt.Errorf("invalid dc_ip %q, expected DC:IP", e)
		}
		dc, err := strconv.Atoi(dcS)
		if err != nil || net.ParseIP(ip) == nil || net.ParseIP(ip).To4() == nil {
			return nil, fmt.Errorf("invalid dc_ip %q", e)
		}
		m[dc] = ip
	}
	return m, nil
}

func CoerceDomainList(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		for _, f := range strings.FieldsFunc(it, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' }) {
			k := strings.ToLower(f)
			if !seen[k] {
				seen[k] = true
				out = append(out, f)
			}
		}
	}
	return out
}

type Stats struct {
	ConnTotal    atomic.Int64
	ConnActive   atomic.Int64
	ConnWS       atomic.Int64
	ConnCF       atomic.Int64
	ConnCFWorker atomic.Int64
	ConnTCP      atomic.Int64
	ConnBad      atomic.Int64
	ConnMasked   atomic.Int64
	WSErrors     atomic.Int64
	PoolHits     atomic.Int64
	PoolMisses   atomic.Int64
	Stalls       atomic.Int64
	connFronting atomic.Int64
	BytesUp      atomic.Int64
	BytesDown    atomic.Int64
}

func HumanBytes(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB"} {
		if f < 1024 {
			return fmt.Sprintf("%.1f%s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.1fTB", f)
}

func (s *Stats) Summary() string {
	return fmt.Sprintf("total=%d active=%d ws=%d cf=%d cfw=%d tcp=%d bad=%d stalls=%d pool=%d/%d up=%s down=%s",
		s.ConnTotal.Load(), s.ConnActive.Load(), s.ConnWS.Load(), s.ConnCF.Load(),
		s.ConnCFWorker.Load(), s.ConnTCP.Load(), s.ConnBad.Load(), s.Stalls.Load(),
		s.PoolHits.Load(), s.PoolHits.Load()+s.PoolMisses.Load(),
		HumanBytes(s.BytesUp.Load()), HumanBytes(s.BytesDown.Load()))
}

// ProxyLink returns the tg:// link for Telegram.
func ProxyLink(cfg Config) string {
	host := linkHost(cfg.Host)
	sec := fmt.Sprintf("%x", cfg.Secret)
	if cfg.FakeTLSDomain != "" {
		return fmt.Sprintf("tg://proxy?server=%s&port=%d&secret=ee%s%x", host, cfg.Port, sec, []byte(cfg.FakeTLSDomain))
	}
	return fmt.Sprintf("tg://proxy?server=%s&port=%d&secret=dd%s", host, cfg.Port, sec)
}

func linkHost(h string) string {
	if h != "0.0.0.0" && h != "" {
		return h
	}
	c, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}
