package main

import (
	_ "embed"
	"os"

	"tgwsproxy/internal/app"
)

//go:embed assets/icon.ico
var iconICO []byte

//go:embed assets/icon.png
var iconPNG []byte

// No arguments: tray app driven by config.json (like the Python TgWsProxy).
// Any proxy flags: plain command-line proxy (like tg_ws_proxy.py).
func main() {
	args := os.Args[1:]
	noTray := len(args) == 1 && args[0] == "--no-tray"
	if len(args) > 0 && !noTray {
		app.AttachParentConsole()
		os.Exit(runCLI(args))
	}
	if !app.SingleInstance() {
		lang := app.DefaultConfig().Language
		app.MessageBox(app.T(lang, "title"), app.T(lang, "already"), false, false)
		return
	}
	a, cfg, first, imported, err := app.New()
	if err != nil {
		app.MessageBox(app.T(cfg.Language, "title"), err.Error(), false, true)
	}
	if noTray {
		app.AttachParentConsole()
		os.Exit(a.RunHeadless(false))
	}
	a.RunTray(iconICO, iconPNG, first, imported)
}
