//go:build android || ios || tgwsmobile

package app

import "errors"

// On phones the native app (Java / Swift) handles links, clipboard and
// dialogs through the settings page bridge; these are no-ops.

func AttachParentConsole()                                     {}
func hasConsole() bool                                         { return false }
func OpenURL(u string)                                         {}
func OpenPath(p string)                                        {}
func CopyToClipboard(text string) error                        { return errors.New("not supported") }
func MessageBox(title, text string, question, isErr bool) bool { return false }
func SingleInstance() bool                                     { return true }

// Autostart on phones is just the config flag; the Android boot receiver
// reads it. iOS has no autostart.
func GetAutostart() bool {
	c, _, _, err := LoadConfig()
	return err == nil && c.Autostart
}

func SetAutostart(on bool) error { return nil }

func systemLanguage() string {
	if MobileLanguage == "ru" || MobileLanguage == "en" {
		return MobileLanguage
	}
	return "en"
}
