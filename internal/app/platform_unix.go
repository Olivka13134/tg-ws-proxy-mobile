//go:build !windows && !android && !ios && !tgwsmobile

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func AttachParentConsole() {}

func hasConsole() bool {
	st, err := os.Stderr.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func OpenURL(u string) {
	if isDarwin() {
		_ = exec.Command("open", u).Start()
		return
	}
	_ = exec.Command("xdg-open", u).Start()
}

func OpenPath(p string) { OpenURL(p) }

func CopyToClipboard(text string) error {
	cands := [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "-b", "-i"}}
	if isDarwin() {
		cands = [][]string{{"pbcopy"}}
	}
	for _, c := range cands {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return errors.New("no clipboard tool found (install wl-clipboard or xclip)")
}

func MessageBox(title, text string, question, isError bool) bool {
	if isDarwin() {
		btn := `buttons {"OK"} default button "OK"`
		if question {
			btn = `buttons {"No", "Yes"} default button "Yes"`
		}
		script := fmt.Sprintf(`display dialog %q with title %q %s`, text, title, btn)
		out, err := exec.Command("osascript", "-e", script).Output()
		return err == nil && strings.Contains(string(out), "Yes")
	}
	if _, err := exec.LookPath("zenity"); err == nil {
		kind := "--info"
		if question {
			kind = "--question"
		} else if isError {
			kind = "--error"
		}
		return exec.Command("zenity", kind, "--title", title, "--text", text, "--no-markup").Run() == nil && question
	}
	if _, err := exec.LookPath("kdialog"); err == nil {
		kind := "--msgbox"
		if question {
			kind = "--yesno"
		} else if isError {
			kind = "--error"
		}
		return exec.Command("kdialog", "--title", title, kind, text).Run() == nil && question
	}
	_ = exec.Command("notify-send", title, text).Start()
	return false
}

var lockFile *os.File

func SingleInstance() bool {
	_ = os.MkdirAll(Dir(), 0o755)
	f, err := os.OpenFile(filepath.Join(Dir(), "app.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	lockFile = f
	return true
}

func autostartPath() string {
	h, _ := os.UserHomeDir()
	if isDarwin() {
		return filepath.Join(h, "Library", "LaunchAgents", "com.tgwsproxygo.plist")
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(h, ".config")
	}
	return filepath.Join(cfg, "autostart", "tgwsproxygo.desktop")
}

func GetAutostart() bool { return fileExists(autostartPath()) }

func SetAutostart(on bool) error {
	p := autostartPath()
	if !on {
		err := os.Remove(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var body string
	if isDarwin() {
		body = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.tgwsproxygo</string>
<key>ProgramArguments</key><array><string>%s</string><string>--no-tray</string></array>
<key>RunAtLoad</key><true/>
</dict></plist>
`, exe)
	} else {
		body = fmt.Sprintf("[Desktop Entry]\nType=Application\nName=TG WS Proxy\nExec=\"%s\"\nX-GNOME-Autostart-enabled=true\n", exe)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(body), 0o644)
}

func systemLanguage() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			if strings.HasPrefix(strings.ToLower(v), "ru") {
				return "ru"
			}
			return "en"
		}
	}
	return "en"
}
