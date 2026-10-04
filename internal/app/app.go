package app

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"syscall"

	"tgwsproxy/internal/proxy"
)

func isWindows() bool { return runtime.GOOS == "windows" }
func isDarwin() bool  { return runtime.GOOS == "darwin" }

// App owns the running proxy and its config.
type App struct {
	mu       sync.Mutex
	cfg      Config
	srv      *proxy.Server
	lastErr  string
	logFile  *proxy.RotatingFile
	onChange func()
	settings *settingsServer
}

func New() (*App, Config, bool, bool, error) {
	cfg, first, imported, err := LoadConfig()
	a := &App{cfg: cfg}
	a.setupLog()
	if imported {
		proxy.Log.Infof("Imported settings from the previous TgWsProxy (same secret and port)")
	}
	return a, cfg, first, imported, err
}

func (a *App) setupLog() {
	f, err := proxy.OpenRotatingFile(LogPath(), int64(a.cfg.LogMaxMB*1024*1024))
	if err == nil {
		a.logFile = f
		if hasConsole() {
			proxy.Log.SetOutput(io.MultiWriter(os.Stderr, f))
		} else {
			proxy.Log.SetOutput(f)
		}
	}
	proxy.Log.Verbose = a.cfg.Verbose
}

func (a *App) Config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

func (a *App) Link() string {
	pc, err := a.Config().ProxyConfig()
	if err != nil {
		return ""
	}
	return proxy.ProxyLink(pc)
}

// Status returns (running, error text, stats).
func (a *App) Status() (bool, string, *proxy.Stats) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv == nil {
		return false, a.lastErr, nil
	}
	return true, "", a.srv.Stats
}

func (a *App) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.startLocked()
}

func (a *App) startLocked() error {
	pc, err := a.cfg.ProxyConfig()
	if err != nil {
		a.lastErr = err.Error()
		proxy.Log.Errorf("Bad config: %v", err)
		return err
	}
	proxy.Log.Verbose = a.cfg.Verbose
	srv := proxy.NewServer(pc)
	if err := srv.Start(); err != nil {
		if isAddrInUse(err) {
			err = fmt.Errorf(T(a.cfg.Language, "port_busy"), a.cfg.Port)
		}
		a.lastErr = err.Error()
		proxy.Log.Errorf("Cannot start proxy: %v", err)
		return err
	}
	a.srv = srv
	a.lastErr = ""
	return nil
}

func isAddrInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		var se *os.SyscallError
		if errors.As(oe.Err, &se) {
			// WSAEADDRINUSE = 10048
			if n, ok := se.Err.(syscall.Errno); ok && n == 10048 {
				return true
			}
		}
	}
	return false
}

func (a *App) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.srv != nil {
		a.srv.Stop()
		proxy.Log.Infof("Proxy stopped. Stats: %s", a.srv.Stats.Summary())
		a.srv = nil
	}
}

// Apply saves a new config and restarts the proxy with it.
func (a *App) Apply(c Config) error {
	c.fill()
	if _, err := c.ProxyConfig(); err != nil {
		return err
	}
	if err := SaveConfig(c); err != nil {
		return err
	}
	a.mu.Lock()
	wasRunning := a.srv != nil
	if a.srv != nil {
		a.srv.Stop()
		a.srv = nil
	}
	a.cfg = c
	var err error
	if wasRunning || !MobileMode {
		err = a.startLocked()
	}
	a.mu.Unlock()
	if GetAutostart() != c.Autostart {
		if aerr := SetAutostart(c.Autostart); aerr != nil {
			proxy.Log.Warnf("autostart: %v", aerr)
		}
	}
	if a.onChange != nil {
		a.onChange()
	}
	return err
}

// Reload re-reads config.json from disk (after a manual edit).
func (a *App) Reload() error {
	c, _, _, err := LoadConfig()
	if err != nil {
		return err
	}
	return a.Apply(c)
}

// SettingsURL starts the local settings page if needed and returns its URL.
func (a *App) SettingsURL() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settings == nil {
		s, err := startSettings(a)
		if err != nil {
			return "", err
		}
		a.settings = s
	}
	return a.settings.url(), nil
}

// LastError is the reason the proxy is not running ("" if none).
func (a *App) LastError() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}

func (a *App) OpenSettings() {
	if a.settings == nil {
		s, err := startSettings(a)
		if err != nil {
			proxy.Log.Errorf("settings page: %v", err)
			OpenPath(ConfigPath())
			return
		}
		a.settings = s
	}
	OpenURL(a.settings.url())
}
