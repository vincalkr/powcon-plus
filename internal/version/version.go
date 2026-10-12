// Package version is the single source of truth for the release version
// and the developer info shown in the window's Info dialog.
package version

// Version is the current release. It can be overridden at build time with:
//
//	-ldflags "-X github.com/vincalkr/powcon-plus/internal/version.Version=1.2.3"
var Version = "0.4.0"

// Developer info shown in the Info dialog/notification.
const (
	Developer = "Vincenzo Calendini"
	Website   = "https://github.com/vincalkr"
)
