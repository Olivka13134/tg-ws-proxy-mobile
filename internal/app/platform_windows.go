//go:build windows

package app

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32                   = windows.NewLazySystemDLL("user32.dll")
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procMessageBoxW          = user32.NewProc("MessageBoxW")
	procOpenClipboard        = user32.NewProc("OpenClipboard")
	procCloseClipboard       = user32.NewProc("CloseClipboard")
	procEmptyClipboard       = user32.NewProc("EmptyClipboard")
	procSetClipboardData     = user32.NewProc("SetClipboardData")
	procGlobalAlloc          = kernel32.NewProc("GlobalAlloc")
	procGlobalLock           = kernel32.NewProc("GlobalLock")
	procGlobalUnlock         = kernel32.NewProc("GlobalUnlock")
	procGlobalFree           = kernel32.NewProc("GlobalFree")
	procAttachConsole        = kernel32.NewProc("AttachConsole")
	procGetUserDefaultUILang = kernel32.NewProc("GetUserDefaultUILanguage")
)

var consoleAttached bool

// AttachParentConsole makes CLI output visible when the GUI exe is started
// from cmd/PowerShell.
func AttachParentConsole() {
	r, _, _ := procAttachConsole.Call(uintptr(0xFFFFFFFF))
	if r == 0 {
		return
	}
	consoleAttached = true
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = f
		os.Stderr = f
	}
}

func hasConsole() bool { return consoleAttached }

func OpenURL(u string) {
	verb, _ := windows.UTF16PtrFromString("open")
	target, _ := windows.UTF16PtrFromString(u)
	_ = windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

func OpenPath(p string) { OpenURL(p) }

func CopyToClipboard(text string) error {
	u, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	if r, _, e := procOpenClipboard.Call(0); r == 0 {
		return e
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	size := uintptr(len(u) * 2)
	h, _, e := procGlobalAlloc.Call(0x0002 /*GMEM_MOVEABLE*/, size)
	if h == 0 {
		return e
	}
	p, _, e := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return e
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(u)), u)
	procGlobalUnlock.Call(h)
	if r, _, e := procSetClipboardData.Call(13 /*CF_UNICODETEXT*/, h); r == 0 {
		procGlobalFree.Call(h)
		return e
	}
	return nil
}

const (
	mbOK        = 0x0
	mbYesNo     = 0x4
	mbIconError = 0x10
	mbIconQuest = 0x20
	mbIconInfo  = 0x40
	mbTopmost   = 0x40000
	mbSetFg     = 0x10000
	idYes       = 6
)

func MessageBox(title, text string, question, isError bool) bool {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	flags := uintptr(mbOK | mbIconInfo)
	if question {
		flags = mbYesNo | mbIconQuest
	} else if isError {
		flags = mbOK | mbIconError
	}
	r, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags|mbTopmost|mbSetFg)
	return r == idYes
}

var instanceMutex windows.Handle

func SingleInstance() bool {
	name, _ := windows.UTF16PtrFromString("Local\\TgWsProxyGo")
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return false
	}
	instanceMutex = h
	return true
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func GetAutostart() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(AppName)
	return err == nil
}

func SetAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		err := k.DeleteValue(AppName)
		if err == syscall.ERROR_FILE_NOT_FOUND {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(AppName, `"`+exe+`"`)
}

func systemLanguage() string {
	r, _, _ := procGetUserDefaultUILang.Call()
	if r&0x3FF == 0x19 { // LANG_RUSSIAN
		return "ru"
	}
	return "en"
}
