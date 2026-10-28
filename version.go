package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version is the taktool version. Release builds can set it with
// -ldflags "-X main.version=2.0.0".
var version = "2.0.0-dev"

// versionString returns the version with the Git commit and date of the
// build, e.g. "taktool 2.0.0-dev (commit 1a2b3c4 2026-10-28, windows/amd64)".
func versionString() string {
	s := "taktool " + version
	var rev, date string
	modified := false
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range bi.Settings {
			switch kv.Key {
			case "vcs.revision":
				rev = kv.Value
			case "vcs.time":
				date = kv.Value
			case "vcs.modified":
				modified = kv.Value == "true"
			}
		}
	}
	details := ""
	if rev != "" {
		if len(rev) > 7 {
			rev = rev[:7]
		}
		details = "commit " + rev
		if len(date) >= 10 {
			details += " " + date[:10]
		}
		if modified {
			details += ", modified"
		}
		details += ", "
	}
	return fmt.Sprintf("%s (%s%s/%s)", s, details, runtime.GOOS, runtime.GOARCH)
}
