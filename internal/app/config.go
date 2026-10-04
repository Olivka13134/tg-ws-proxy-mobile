package app

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tgwsproxy/internal/proxy"
)

const (
	AppName     = "TgWsProxyGo"
	oldAppName  = "TgWsProxy" // the Python app, settings are imported from it
	portableDir = "TgWsProxyGo_data"
)

// Config is stored as JSON. Keys match the Python app's config.json so old
// settings (and the secret already added to Telegram) carry over.
type Config struct {
	Port                     int      `json:"port"`
	Host                     string   `json:"host"`
	Secret                   string   `json:"secret"`
	DCIP                     []string `json:"dc_ip"`
	Verbose                  bool     `json:"verbose"`
	LogMaxMB                 float64  `json:"log_max_mb"`
	BufKB                    int      `json:"buf_kb"`
	PoolSize                 int      `json:"pool_size"`
	CFProxy                  bool     `json:"cfproxy"`
	CFProxyUserDomainEnabled bool     `json:"cfproxy_user_domain_enabled"`
	CFProxyUserDomain        domains  `json:"cfproxy_user_domain"`
	CFProxyWorkerEnabled     bool     `json:"cfproxy_worker_enabled"`
	CFProxyWorkerDomain      domains  `json:"cfproxy_worker_domain"`
	ForceTestDC              bool     `json:"force_test_dc"`
	NoSecure                 bool     `json:"no_secure"`
	FakeTLSDomain            string   `json:"fake_tls_domain"`
	StallTimeout             float64  `json:"stall_timeout"`
	FallbackHeadMS           int      `json:"fallback_head_ms"`
	Language                 string   `json:"language"`
	Autostart                bool     `json:"autostart"`
}

// domains accepts both a list and a single comma/space separated string
// (older Python configs stored a string).
type domains []string

func (d *domains) UnmarshalJSON(b []byte) error {
	var l []string
	if err := json.Unmarshal(b, &l); err == nil {
		*d = proxy.CoerceDomainList(l)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*d = proxy.CoerceDomainList([]string{s})
		return nil
	}
	*d = nil
	return nil
}

func DefaultConfig() Config {
	return Config{
		Port:           1443,
		Host:           "127.0.0.1",
		Secret:         hex.EncodeToString(randomSecret()),
		DCIP:           []string{"2:149.154.167.220", "4:149.154.167.220"},
		LogMaxMB:       5,
		BufKB:          256,
		PoolSize:       4,
		CFProxy:        true,
		StallTimeout:   5,
		FallbackHeadMS: 1000,
		Language:       systemLanguage(),
	}
}

func randomSecret() []byte {
	c := proxy.DefaultConfig()
	return c.Secret
}

func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	p, _ = filepath.EvalSymlinks(p)
	return filepath.Dir(p)
}

func standardDir(name string) string {
	switch {
	case isWindows():
		if a := os.Getenv("APPDATA"); a != "" {
			return filepath.Join(a, name)
		}
	case isDarwin():
		h, _ := os.UserHomeDir()
		return filepath.Join(h, "Library", "Application Support", name)
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, name)
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", name)
}

// DataDirOverride is set by the phone apps (their private files folder).
var DataDirOverride string

// MobileLanguage is set by the phone apps before the config is created.
var MobileLanguage = "ru"

// MobileMode: proxy runs only when the user turned it on (foreground
// service / background audio), so saving settings must not start it.
var MobileMode bool

// Dir is where config.json and the log live. A "TgWsProxyGo_data" folder
// next to the exe switches to portable mode.
func Dir() string {
	if DataDirOverride != "" {
		return DataDirOverride
	}
	if d := exeDir(); d != "" {
		p := filepath.Join(d, portableDir)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return standardDir(AppName)
}

func ConfigPath() string { return filepath.Join(Dir(), "config.json") }
func LogPath() string    { return filepath.Join(Dir(), "proxy.log") }

// LoadConfig reads config.json; on first run it imports the Python app's
// settings if present. Returns imported=true in that case.
func LoadConfig() (cfg Config, firstRun, imported bool, err error) {
	cfg = DefaultConfig()
	b, rerr := os.ReadFile(ConfigPath())
	if rerr != nil {
		firstRun = true
		old := filepath.Join(standardDir(oldAppName), "config.json")
		if d := exeDir(); d != "" {
			if p := filepath.Join(d, "TgWsProxy_data", "config.json"); fileExists(p) {
				old = p
			}
		}
		if ob, oerr := os.ReadFile(old); oerr == nil {
			if json.Unmarshal(ob, &cfg) == nil {
				imported = true
			}
			cfg.Autostart = false // the old app owns its own autostart entry
		}
		cfg.fill()
		err = SaveConfig(cfg)
		return
	}
	if err = json.Unmarshal(b, &cfg); err != nil {
		return DefaultConfig(), false, false, fmt.Errorf("config.json: %w", err)
	}
	cfg.fill()
	return
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// fill repairs missing/invalid values.
func (c *Config) fill() {
	d := DefaultConfig()
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = d.Port
	}
	if strings.TrimSpace(c.Host) == "" {
		c.Host = d.Host
	}
	if b, err := hex.DecodeString(c.Secret); err != nil || len(b) != 16 {
		c.Secret = d.Secret
	}
	if c.BufKB <= 0 {
		c.BufKB = d.BufKB
	}
	if c.LogMaxMB <= 0 {
		c.LogMaxMB = d.LogMaxMB
	}
	if c.PoolSize < 0 {
		c.PoolSize = 0
	}
	if c.StallTimeout < 0 {
		c.StallTimeout = 0
	}
	if c.FallbackHeadMS <= 0 {
		c.FallbackHeadMS = d.FallbackHeadMS
	}
	if c.Language != "ru" && c.Language != "en" {
		c.Language = d.Language
	}
	if c.DCIP == nil {
		c.DCIP = []string{}
	}
	if c.CFProxyUserDomain == nil {
		c.CFProxyUserDomain = domains{}
	}
	if c.CFProxyWorkerDomain == nil {
		c.CFProxyWorkerDomain = domains{}
	}
}

func SaveConfig(c Config) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := ConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ConfigPath())
}

func (c Config) ProxyConfig() (proxy.Config, error) {
	pc := proxy.DefaultConfig()
	pc.Host, pc.Port = c.Host, c.Port
	sec, err := hex.DecodeString(c.Secret)
	if err != nil || len(sec) != 16 {
		return pc, fmt.Errorf("secret must be 32 hex characters")
	}
	pc.Secret = sec
	m, err := proxy.ParseDCIPList(c.DCIP)
	if err != nil {
		return pc, err
	}
	pc.DCRedirects = m
	pc.BufferSize = max(4, c.BufKB) * 1024
	pc.PoolSize = max(0, c.PoolSize)
	pc.CFProxy = c.CFProxy
	if c.CFProxyUserDomainEnabled {
		pc.CFUserDomains = c.CFProxyUserDomain
	}
	if c.CFProxyWorkerEnabled {
		pc.CFWorkers = c.CFProxyWorkerDomain
	}
	pc.NoSecure = c.NoSecure
	pc.ForceTestDC = c.ForceTestDC
	pc.FakeTLSDomain = strings.TrimSpace(c.FakeTLSDomain)
	pc.StallTimeout = time.Duration(c.StallTimeout * float64(time.Second))
	pc.FallbackHead = time.Duration(c.FallbackHeadMS) * time.Millisecond
	return pc, nil
}
