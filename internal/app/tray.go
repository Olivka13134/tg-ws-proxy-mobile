//go:build (windows || linux) && !android && !tgwsmobile

package app

import (
	"fmt"
	"time"

	"fyne.io/systray"

	"tgwsproxy/internal/proxy"
)

// RunTray runs the app with a tray icon until the user quits.
func (a *App) RunTray(iconICO, iconPNG []byte, firstRun, imported bool) {
	systray.Run(func() { a.trayReady(iconICO, iconPNG, firstRun, imported) }, func() { a.Stop() })
}

func (a *App) trayReady(iconICO, iconPNG []byte, firstRun, imported bool) {
	if isWindows() {
		systray.SetIcon(iconICO)
	} else {
		systray.SetIcon(iconPNG)
	}
	lang := a.Config().Language
	systray.SetTitle("")
	systray.SetTooltip(T(lang, "title"))

	mStatus := systray.AddMenuItem(T(lang, "title"), "")
	mStatus.Disable()
	mStats := systray.AddMenuItem("", "")
	mStats.Disable()
	systray.AddSeparator()
	mOpen := systray.AddMenuItem(T(lang, "open_tg"), "")
	mCopy := systray.AddMenuItem(T(lang, "copy_link"), "")
	systray.AddSeparator()
	mSettings := systray.AddMenuItem(T(lang, "settings"), "")
	mRestart := systray.AddMenuItem(T(lang, "restart"), "")
	mLog := systray.AddMenuItem(T(lang, "open_log"), "")
	mFolder := systray.AddMenuItem(T(lang, "open_folder"), "")
	var mAuto *systray.MenuItem
	if isWindows() {
		mAuto = systray.AddMenuItemCheckbox(T(lang, "autostart"), "", GetAutostart())
	}
	systray.AddSeparator()
	mQuit := systray.AddMenuItem(T(lang, "quit"), "")

	refresh := func() {
		c := a.Config()
		lang := c.Language
		running, errText, st := a.Status()
		if running {
			mStatus.SetTitle(fmt.Sprintf(T(lang, "running"), c.Host, c.Port))
			mStats.SetTitle(fmt.Sprintf(T(lang, "stats"), st.ConnActive.Load(),
				proxy.HumanBytes(st.BytesUp.Load()), proxy.HumanBytes(st.BytesDown.Load())))
			mStats.Show()
			systray.SetTooltip(fmt.Sprintf("%s\n%s", T(lang, "title"),
				fmt.Sprintf(T(lang, "stats"), st.ConnActive.Load(),
					proxy.HumanBytes(st.BytesUp.Load()), proxy.HumanBytes(st.BytesDown.Load()))))
		} else {
			mStatus.SetTitle(T(lang, "stopped"))
			if errText != "" {
				mStats.SetTitle(trim(errText, 60))
				mStats.Show()
			} else {
				mStats.Hide()
			}
			systray.SetTooltip(T(lang, "title") + " — " + T(lang, "stopped"))
		}
		if mAuto != nil {
			if GetAutostart() {
				mAuto.Check()
			} else {
				mAuto.Uncheck()
			}
		}
	}
	a.onChange = refresh

	startErr := a.Start()
	refresh()
	go func() {
		if startErr != nil {
			MessageBox(T(lang, "title"), fmt.Sprintf(T(lang, "start_error"), startErr), false, true)
			return
		}
		if firstRun {
			key := "first_run"
			if imported {
				key = "first_run_imp"
			}
			if MessageBox(T(lang, "title"), T(lang, key), true, false) {
				OpenURL(a.Link())
			}
		}
	}()

	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				refresh()
			case <-mOpen.ClickedCh:
				OpenURL(a.Link())
			case <-mCopy.ClickedCh:
				if err := CopyToClipboard(a.Link()); err != nil {
					proxy.Log.Warnf("clipboard: %v", err)
					MessageBox(T(lang, "title"), a.Link(), false, false)
				}
			case <-mSettings.ClickedCh:
				a.OpenSettings()
			case <-mRestart.ClickedCh:
				go func() {
					if err := a.Reload(); err != nil {
						MessageBox(T(lang, "title"), fmt.Sprintf(T(lang, "start_error"), err), false, true)
					}
					refresh()
				}()
			case <-mLog.ClickedCh:
				OpenPath(LogPath())
			case <-mFolder.ClickedCh:
				OpenPath(Dir())
			case <-autoCh(mAuto):
				on := !GetAutostart()
				if err := SetAutostart(on); err != nil {
					proxy.Log.Warnf("autostart: %v", err)
				}
				c := a.Config()
				c.Autostart = on
				_ = SaveConfig(c)
				a.mu.Lock()
				a.cfg.Autostart = on
				a.mu.Unlock()
				refresh()
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func autoCh(m *systray.MenuItem) <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.ClickedCh
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
