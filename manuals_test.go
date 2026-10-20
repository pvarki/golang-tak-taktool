package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestManualNames(t *testing.T) {
	for in, want := range map[string]string{
		"ATAK_User_Guide.pdf":    "UserGuide",
		"ATAK_Icon_Glossary.pdf": "IconGlossary",
		"release-notes.pdf":      "ReleaseNotes",
	} {
		if got := docType(in); got != want {
			t.Errorf("docType(%q) = %q, want %q", in, got, want)
		}
	}
	if got := compactName("Data  Sync"); got != "DataSync" {
		t.Errorf("compactName = %q", got)
	}
	if got := majorMinor("5.8.0"); got != "5.8" {
		t.Errorf("majorMinor = %q", got)
	}
	if got := safeFileName(`A/B: "C"?.pdf`); got != "AB C.pdf" {
		t.Errorf("safeFileName = %q", got)
	}
}

func writeZip(t *testing.T, p string, files map[string]string) {
	t.Helper()
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	for n, c := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(c))
	}
	zw.Close()
	f.Close()
}

func TestManualsInApk(t *testing.T) {
	dir := t.TempDir()
	plugin := filepath.Join(dir, "ds.apk")
	writeZip(t, plugin, map[string]string{
		"assets/usermanual.pdf":                "new",
		"assets/ATAK_Data Sync User Guide.pdf": "old",
		"classes.dex":                          "x",
	})
	atak := filepath.Join(dir, "atak.apk")
	writeZip(t, atak, map[string]string{
		"assets/support/docs/ATAK_User_Guide.pdf": "guide",
		"assets/support/license/x.pdf":            "license",
	})

	ds := &repoApk{path: plugin, info: ApkInfo{Package: "com.atakmap.android.datasync.plugin", DisplayName: "Data Sync", Version: "5.0.2 (220e4ef7) - [5.8.0]"}}
	m, others, err := manualsInApk(ds, "5.8.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || m[0].name != "ATAK 5.8 DataSync 5.0.2 UserGuide.pdf" {
		t.Errorf("manuals %+v", m)
	}
	if !slices.Equal(others, []string{"ATAK_Data Sync User Guide.pdf"}) {
		t.Errorf("others %v", others)
	}
	m, _, _ = manualsInApk(ds, "5.8.0", true)
	if len(m) != 2 || m[1].name != "DataSync ATAK_Data Sync User Guide.pdf" && m[0].name != "DataSync ATAK_Data Sync User Guide.pdf" {
		t.Errorf("allmanuals %+v", m)
	}

	a := &repoApk{path: atak, info: ApkInfo{Package: "com.atakmap.app.civ", DisplayName: "ATAK", Version: "5.8.0.3 (x)"}}
	m, others, _ = manualsInApk(a, "5.8.0", false)
	if len(m) != 1 || m[0].name != "ATAK 5.8 UserGuide.pdf" || len(others) != 1 {
		t.Errorf("atak manuals %+v others %v", m, others)
	}

	// Up to date check and writing
	out := filepath.Join(dir, "manuals")
	if s, _ := manualState(filepath.Join(out, m[0].name), m[0]); s != "add" {
		t.Errorf("state %q, want add", s)
	}
	if err := writeManuals(out, m); err != nil {
		t.Fatal(err)
	}
	if s, _ := manualState(filepath.Join(out, m[0].name), m[0]); s != "" {
		t.Errorf("state %q after writing, want up to date", s)
	}
	os.WriteFile(filepath.Join(out, m[0].name), []byte("other"), 0o644)
	if s, _ := manualState(filepath.Join(out, m[0].name), m[0]); s != "update" {
		t.Errorf("state %q after change, want update", s)
	}
}

func TestConfigManuals(t *testing.T) {
	dir := t.TempDir()
	c, err := loadConfig(writeConfig(t, dir, "versions: [5.8.0]\nmanuals: docs\n"))
	if err != nil || c.Manuals != filepath.Join(dir, "docs") {
		t.Errorf("manuals %q, %v", c.Manuals, err)
	}
	for _, bad := range []string{
		"versions: [5.8.0]\noutput: up\nmanuals: up/docs\n",
		"versions: [5.8.0]\nallmanuals: true\n",
	} {
		if _, err := loadConfig(writeConfig(t, dir, bad)); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
}
