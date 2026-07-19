package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

// ANSI 256-color codes in a B&O-ish palette: warm bronze on graphite.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiBronze = "\x1b[38;5;179m"
	ansiCopper = "\x1b[38;5;137m"
	ansiGray   = "\x1b[38;5;245m"
)

// printBanner writes the startup art. Colors only when w is a terminal
// (and NO_COLOR is unset), so log collectors get clean plain text.
func printBanner(w io.Writer) {
	c := func(code, s string) string { return code + s + ansiReset }
	if !wantColor(w) {
		c = func(_, s string) string { return s }
	}

	// The grille: one perforation empty, its dot escaped toward the
	// broker (docs/logo.svg is the same mark in SVG).
	lines := []string{
		"",
		"  " + c(ansiGray, "· · · · ·"),
		"  " + c(ansiGray, "· · · ·") + "   " + c(ansiBronze, "●") + "   " + c(ansiBold, "beomqtt") + " " + c(ansiBronze, version),
		"  " + c(ansiGray, "· · · · ·") + "     " + c(ansiGray, "Bang & Olufsen Mozart → MQTT"),
		"",
	}
	fmt.Fprint(w, strings.Join(lines, "\n")+"\n")
}

func wantColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
