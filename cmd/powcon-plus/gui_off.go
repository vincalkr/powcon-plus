//go:build !gui

package main

import "fmt"

// cmdWindow is compiled only with -tags gui (see gui_on.go).
func cmdWindow(args []string) error {
	return fmt.Errorf("binario costruito senza supporto grafico: ricostruire con `make gui` (richiede OpenGL/X11)")
}
