// Package tgws is the API the Android (Java) and iOS (Swift) apps call.
// Built with gomobile: Java class tgws.Tgws, Objective-C/Swift prefix Tgws.
package tgws

import (
	"fmt"
	"sync"

	"tgwsproxy/internal/app"
	"tgwsproxy/internal/proxy"
)

var (
	mu sync.Mutex
	a  *app.App
)

// Init loads (or creates) the config in dataDir and prepares the app.
// lang is "ru" or "en" (used for a fresh config). Safe to call repeatedly.
func Init(dataDir, lang string) error {
	mu.Lock()
	defer mu.Unlock()
	if a != nil {
		return nil
	}
	app.DataDirOverride = dataDir
	app.MobileMode = true
	app.MobileLanguage = lang
	na, _, _, _, err := app.New()
	a = na
	return err
}

func get() (*app.App, error) {
	mu.Lock()
	defer mu.Unlock()
	if a == nil {
		return nil, fmt.Errorf("tgws: Init was not called")
	}
	return a, nil
}

// StartProxy starts the proxy (no-op if it already runs).
func StartProxy() error {
	x, err := get()
	if err != nil {
		return err
	}
	if running, _, _ := x.Status(); running {
		return nil
	}
	return x.Start()
}

func StopProxy() {
	if x, err := get(); err == nil {
		x.Stop()
	}
}

func IsRunning() bool {
	x, err := get()
	if err != nil {
		return false
	}
	r, _, _ := x.Status()
	return r
}

// SettingsURL returns the local settings page (http://127.0.0.1:port/token/).
func SettingsURL() (string, error) {
	x, err := get()
	if err != nil {
		return "", err
	}
	return x.SettingsURL()
}

// Link is the tg://proxy link for Telegram.
func Link() string {
	x, err := get()
	if err != nil {
		return ""
	}
	return x.Link()
}

// LastError explains why the proxy is not running ("" if none).
func LastError() string {
	x, err := get()
	if err != nil {
		return err.Error()
	}
	return x.LastError()
}

// StatusLine is a short text for the notification.
func StatusLine() string {
	x, err := get()
	if err != nil {
		return ""
	}
	running, e, st := x.Status()
	ru := x.Config().Language == "ru"
	if !running {
		if e != "" {
			return e
		}
		if ru {
			return "Остановлен"
		}
		return "Stopped"
	}
	f := "Connections: %d · ↑ %s ↓ %s"
	if ru {
		f = "Соединений: %d · ↑ %s ↓ %s"
	}
	return fmt.Sprintf(f, st.ConnActive.Load(), proxy.HumanBytes(st.BytesUp.Load()), proxy.HumanBytes(st.BytesDown.Load()))
}

// Language of the UI ("ru" / "en") as saved in settings.
func Language() string {
	x, err := get()
	if err != nil {
		return "ru"
	}
	return x.Config().Language
}

// AutostartEnabled tells the Android boot receiver whether to start.
// Reads the config from dataDir without starting anything.
func AutostartEnabled(dataDir string) bool {
	app.DataDirOverride = dataDir
	app.MobileMode = true
	return app.GetAutostart()
}
