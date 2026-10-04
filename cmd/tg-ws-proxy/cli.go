package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tgwsproxy/internal/proxy"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// runCLI mirrors the Python command line (tg_ws_proxy.py) plus two new knobs.
func runCLI(args []string) int {
	fs := flag.NewFlagSet("tg-ws-proxy", flag.ContinueOnError)
	port := fs.Int("port", 1443, "Listen port")
	host := fs.String("host", "127.0.0.1", "Listen host")
	secret := fs.String("secret", "", "MTProto proxy secret (32 hex chars), auto-generated if empty")
	var dcIP, cfDomains, cfWorkers multiFlag
	fs.Var(&dcIP, "dc-ip", "Target IP for a DC, e.g. --dc-ip 2:149.154.167.220 (repeatable; 'none' for empty)")
	verbose := fs.Bool("v", false, "Debug logging")
	fs.BoolVar(verbose, "verbose", false, "Debug logging")
	logFile := fs.String("log-file", "", "Log to file with rotation")
	logMax := fs.Float64("log-max-mb", 5, "Max log file size in MB")
	bufKB := fs.Int("buf-kb", 256, "Socket buffer size in KB")
	poolSize := fs.Int("pool-size", 4, "WS connection pool size per DC")
	fs.Var(&cfDomains, "cfproxy-domain", "Own Cloudflare-proxied domain (repeatable)")
	fs.Var(&cfWorkers, "cfproxy-worker-domain", "Cloudflare Worker domain (repeatable)")
	noCF := fs.Bool("no-cfproxy", false, "Disable Cloudflare proxy fallback")
	noSecure := fs.Bool("no-secure", false, "Use port 80 for CF proxy / worker")
	fakeTLS := fs.String("fake-tls-domain", "", "Enable Fake TLS (ee-secret) masking with this SNI domain")
	forceTest := fs.Bool("force-test-dc", false, "Force all traffic to Telegram test DCs")
	proxyProto := fs.Bool("proxy-protocol", false, "Accept PROXY protocol v1 header")
	stall := fs.Float64("stall-timeout", 5, "Drop a session when Telegram stops ACKing data for N seconds (0 = off)")
	head := fs.Int("fallback-head-ms", 1000, "Direct WS head start before fallbacks race in parallel")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg := proxy.DefaultConfig()
	cfg.Port, cfg.Host = *port, *host
	if *secret != "" {
		b, err := hex.DecodeString(strings.TrimSpace(*secret))
		if err != nil || len(b) != 16 {
			fmt.Fprintln(os.Stderr, "Secret must be exactly 32 hex characters")
			return 1
		}
		cfg.Secret = b
	} else {
		proxy.Log.Infof("Generated secret: %x", cfg.Secret)
	}
	if len(dcIP) > 0 {
		list := []string{}
		for _, d := range dcIP {
			if d != "none" && d != "" {
				list = append(list, d)
			}
		}
		m, err := proxy.ParseDCIPList(list)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cfg.DCRedirects = m
	}
	cfg.BufferSize = max(4, *bufKB) * 1024
	cfg.PoolSize = max(0, *poolSize)
	cfg.CFProxy = !*noCF
	cfg.CFUserDomains = proxy.CoerceDomainList(cfDomains)
	cfg.CFWorkers = proxy.CoerceDomainList(cfWorkers)
	cfg.NoSecure = *noSecure
	cfg.FakeTLSDomain = strings.TrimSpace(*fakeTLS)
	cfg.ForceTestDC = *forceTest
	cfg.ProxyProtocol = *proxyProto
	cfg.StallTimeout = time.Duration(*stall * float64(time.Second))
	cfg.FallbackHead = time.Duration(*head) * time.Millisecond

	proxy.Log.Verbose = *verbose
	if *logFile != "" {
		f, err := proxy.OpenRotatingFile(*logFile, int64(*logMax*1024*1024))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		proxy.Log.SetOutput(teeWriter{os.Stderr, f})
	}

	srv := proxy.NewServer(cfg)
	if err := srv.Start(); err != nil {
		proxy.Log.Errorf("Cannot start: %v", err)
		return 1
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	srv.Stop()
	proxy.Log.Infof("Shutting down. Final stats: %s", srv.Stats.Summary())
	return 0
}

type teeWriter struct{ a, b interface{ Write([]byte) (int, error) } }

func (t teeWriter) Write(p []byte) (int, error) {
	t.a.Write(p)
	return t.b.Write(p)
}
