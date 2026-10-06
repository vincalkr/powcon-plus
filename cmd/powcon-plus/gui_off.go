//go:build !gui

package main

import "fmt"

// cmdWindow is compiled only with -tags gui (see gui_on.go).
func cmdWindow(args []string) error {
	return fmt.Errorf("binary built without GUI support: rebuild with `make gui` (requires OpenGL/X11)")
}
