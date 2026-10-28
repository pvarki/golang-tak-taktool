package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	c, err := loadConfig(writeConfig(t, dir, "versions: [5.8.0]\nmanuals:\n  folder: docs\n  all: true\n"))
	if err != nil || c.Manuals.Folder != filepath.Join(dir, "docs") || !c.Manuals.All || c.Manuals.WithRepo {
		t.Errorf("manuals %+v, %v", c.Manuals, err)
	}
	for _, bad := range []string{
		"versions: [5.8.0]\noutput: up\nmanuals:\n  folder: up/docs\n",
		"versions: [5.8.0]\nmanuals:\n  withrepo: true\n",
		"versions: [5.8.0]\nmanuals: docs\n", // old form
	} {
		if _, err := loadConfig(writeConfig(t, dir, bad)); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
}

func TestOneManualForAnApkInSeveralVersions(t *testing.T) {
	dir := t.TempDir()
	apk := filepath.Join(dir, "helmcam.apk")
	writeZip(t, apk, map[string]string{"assets/usermanual.pdf": "guide"})
	a := &repoApk{path: apk, info: ApkInfo{Package: "com.atakmap.helmcam", DisplayName: "HelmCam", Version: "7.0.1 (8123dd5)", Hash: "h"}}
	p := &repoPlan{cfg: &Config{Manuals: ManualsConfig{Folder: filepath.Join(dir, "manuals")}}}
	for _, v := range []string{"5.6.0", "5.8.0", "5.7.0"} {
		p.versions = append(p.versions, &versionPlan{version: v, apks: map[string]*repoApk{a.info.Package: a}})
	}
	if err := p.planManuals(); err != nil {
		t.Fatal(err)
	}
	if len(p.manuals) != 1 || p.manuals[0].name != "ATAK 5.8 HelmCam 7.0.1 UserGuide.pdf" {
		t.Errorf("manuals %+v", p.manuals)
	}
}

func TestInitFolder(t *testing.T) {
	base := t.TempDir()
	var out bytes.Buffer
	if err := InitFolder(&out, base); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"apks/images", "update", "manuals"} {
		if st, err := os.Stat(filepath.Join(base, d)); err != nil || !st.IsDir() {
			t.Errorf("%s not created", d)
		}
	}
	cfgPath := filepath.Join(base, "apks", configFileName)
	c, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("created %s does not load: %v", configFileName, err)
	}
	if c.Manuals.WithRepo || c.Output != filepath.Join(base, "update") || c.Manuals.Folder != filepath.Join(base, "manuals") {
		t.Errorf("config %+v", c)
	}

	// A second run changes nothing
	os.WriteFile(cfgPath, []byte("versions: [5.8.0]\n"), 0o644)
	out.Reset()
	if err := InitFolder(&out, base); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfgPath); string(b) != "versions: [5.8.0]\n" {
		t.Error("existing taktool.yaml was changed")
	}
	if strings.Contains(out.String(), "created:") {
		t.Errorf("second run created something:\n%s", out.String())
	}
}

func TestConfigSyntaxError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "apks")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, configFileName)
	if err := os.WriteFile(path, []byte("versions: [5.8.0]\nmanuals: ../manuals\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"has an error", "taktool.example.yaml", "'taktool init ..'"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not contain %q:\n%v", want, err)
		}
	}
}

func TestInitRefusesApkFolder(t *testing.T) {
	base := t.TempDir()
	var out strings.Builder
	if err := InitFolder(&out, base); err != nil {
		t.Fatal(err)
	}
	// Again inside apks/: refused, nothing created there
	apks := filepath.Join(base, "apks")
	err := InitFolder(&out, apks)
	if err == nil || !strings.Contains(err.Error(), "apk folder already") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(apks, "update")); !os.IsNotExist(err) {
		t.Error("update/ created inside the apk folder")
	}
}
