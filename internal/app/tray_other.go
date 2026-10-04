//go:build !windows && (!linux || android || tgwsmobile)

package app

// No tray on this platform (macOS builds are made without cgo): run
// headless and open the settings page on first run.
func (a *App) RunTray(iconICO, iconPNG []byte, firstRun, imported bool) {
	a.RunHeadless(firstRun)
}
