//go:build tools

package tgws

// Keeps golang.org/x/mobile in go.mod: gomobile bind needs it.
import _ "golang.org/x/mobile/bind"
