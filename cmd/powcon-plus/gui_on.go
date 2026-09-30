//go:build gui

package main

import (
	"flag"

	"powcon-plus/internal/gui"
)

// cmdWindow opens the detail window (only present in -tags gui builds).
func cmdWindow(args []string) error {
	fs := flag.NewFlagSet("window", flag.ContinueOnError)
	configPath := fs.String("config", "", "file di configurazione")
	dataDir := fs.String("data-dir", "", "data dir del daemon (default: quella in configurazione)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return gui.Run(gui.Options{ConfigPath: *configPath, DataDir: *dataDir})
}
