package main

import (
	"archive/zip"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	// Value of the plugin-api meta-data, e.g. "com.atakmap.app@5.8.0.CIV".
	// ATAK itself has it too.
	takReqRe = regexp.MustCompile(`^com\.atakmap\.app@(\d+\.\d+\.\d+)(?:\.([A-Za-z]+))?$`)
	// ATAK version at the end of a plugin's versionName, e.g. "4.0 (ac524e2c) - [5.8.0]"
	bracketVersionRe = regexp.MustCompile(`\[(\d+\.\d+\.\d+)\]`)
	// ATAK's own versionName, e.g. "5.8.0.3 (4f67063)[playstore]"
	leadingVersionRe = regexp.MustCompile(`^(\d+\.\d+\.\d+)`)
	// ATAK version followed by the flavor in a file name,
	// e.g. "ATAK-Plugin-vns-4.2-4bb952de-5.6.0-civ-release.apk". The version
	// must not be the end of a longer number ("ATAKGo-4.18.0.190-CIV").
	fileVersionRe = regexp.MustCompile(`(?i)(?:^|[-_ ])(\d+\.\d+\.\d+)[-_ .](civ|mil|gov)(?:[-_ .]|$)`)
	// Flavor as a separate word in a file name, e.g. "atak-mil-release.apk"
	fileFlavorRe = regexp.MustCompile(`(?i)(?:^|[-_ .])(civ|mil|gov)(?:[-_ .]|$)`)
	versionRe    = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// atakTarget is the ATAK version and flavor an apk is made for.
type atakTarget struct {
	Version string // e.g. "5.8.0"; empty if not found
	Flavor  string // "CIV", "MIL", "GOV"; empty if not found
	Source  string // where the version was found, for messages
}

// isAtakApp reports whether the package is ATAK itself (e.g. com.atakmap.app.civ).
// System plugins such as com.atakmap.app.flavor.* and
// com.atakmap.app.encryption.* are not ATAK itself.
func isAtakApp(pkg string) bool {
	if pkg == "com.atakmap.app" {
		return true
	}
	rest, ok := strings.CutPrefix(pkg, "com.atakmap.app.")
	return ok && rest != "" && !strings.Contains(rest, ".")
}

// detectAtakTarget finds the ATAK version an apk is made for, in this order:
//  1. the plugin-api meta-data (com.atakmap.app@5.8.0.CIV)
//  2. the versionName ("... - [5.8.0]", or ATAK's own "5.8.0.3 ...")
//  3. the file name ("...-5.6.0-civ-release.apk")
func detectAtakTarget(a ApkInfo, fileName string) atakTarget {
	var t atakTarget

	if m := takReqRe.FindStringSubmatch(a.TakReq); m != nil {
		t.Version, t.Flavor, t.Source = m[1], strings.ToUpper(m[2]), "plugin-api"
	}
	if t.Version == "" {
		if m := bracketVersionRe.FindStringSubmatch(a.Version); m != nil {
			t.Version, t.Source = m[1], "versionName"
		} else if isAtakApp(a.Package) {
			if m := leadingVersionRe.FindStringSubmatch(a.Version); m != nil {
				t.Version, t.Source = m[1], "versionName"
			}
		}
	}
	if m := fileVersionRe.FindStringSubmatch(fileName); m != nil {
		if t.Version == "" {
			t.Version, t.Source = m[1], "file name"
		}
		if t.Flavor == "" {
			t.Flavor = strings.ToUpper(m[2])
		}
	}

	if t.Flavor == "" && isAtakApp(a.Package) {
		switch {
		case strings.HasSuffix(a.Package, ".civ"):
			t.Flavor = "CIV"
		case strings.HasSuffix(a.Package, ".mil"):
			t.Flavor = "MIL"
		case strings.HasSuffix(a.Package, ".gov"):
			t.Flavor = "GOV"
		}
	}
	if t.Flavor == "" {
		if m := fileFlavorRe.FindStringSubmatch(fileName); m != nil {
			t.Flavor = strings.ToUpper(m[1])
		}
	}
	return t
}

// compareVersions compares dotted version numbers such as "5.10.0" and "5.9.0".
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// shortVersion returns the first word of a versionName,
// e.g. "4.0" from "4.0 (ac524e2c) - [5.8.0]".
func shortVersion(v string) string {
	if i := strings.IndexAny(v, " ([-"); i > 0 {
		return v[:i]
	}
	return v
}

const (
	atakSmall = "small"
	atakFull  = "full"
)

// detectAtakVariant tells the full ATAK apk (civ-release) from the small one
// (civSmall-release). Both have the same package name, versionCode and
// signing certificate. The full apk also has 32-bit ARM and x86 native
// libraries and bundles apps under assets/apks; the small one has only
// arm64-v8a libraries and no bundled apps. Its versionName ends with
// "[playstore]". The content decides; warning is set if the versionName
// disagrees with it.
func detectAtakVariant(path string, info ApkInfo) (variant, warning string, err error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", "", err
	}
	defer zr.Close()

	variant = atakSmall
	for _, f := range zr.File {
		n := f.Name
		if strings.HasPrefix(n, "lib/armeabi-v7a/") || strings.HasPrefix(n, "lib/x86/") ||
			(strings.HasPrefix(n, "assets/apks/") && strings.HasSuffix(strings.ToLower(n), ".apk")) {
			variant = atakFull
			break
		}
	}

	playstore := strings.Contains(info.Version, "[playstore]")
	if (variant == atakSmall) != playstore {
		warning = fmt.Sprintf("content looks like ATAK %s but versionName %q says otherwise", variant, info.Version)
	}
	return variant, warning, nil
}
