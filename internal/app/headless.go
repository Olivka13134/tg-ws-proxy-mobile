package app

import (
	"os"
	"os/signal"
	"syscall"

	"tgwsproxy/internal/proxy"
)

// RunHeadless runs the app from config.json without a tray icon
// (macOS builds, servers, or --no-tray). The settings page still works.
func (a *App) RunHeadless(openSettings bool) int {
	if err := a.Start(); err != nil {
		proxy.Log.Errorf("%v", err)
	}
	if st, err := startSettings(a); err == nil {
		a.settings = st
		proxy.Log.Infof("Settings page: %s", st.url())
		if openSettings {
			OpenURL(st.url())
		}
	}
	proxy.Log.Infof("Config file: %s", ConfigPath())
	proxy.Log.Infof("Add to Telegram: %s", a.Link())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	a.Stop()
	return 0
}
