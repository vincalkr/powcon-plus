//go:build gui

package main

import (
	"flag"

	"github.com/vincalkr/powcon-plus/internal/gui"
)

// cmdWindow opens the detail window (only present in -tags gui builds).
func cmdWindow(args []string) error {
	fs := flag.NewFlagSet("window", flag.ContinueOnError)
	configPath := fs.String("config", "", "configuration file")
	dataDir := fs.String("data-dir", "", "daemon data dir (default: the one from the configuration)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return gui.Run(gui.Options{ConfigPath: *configPath, DataDir: *dataDir})
}
