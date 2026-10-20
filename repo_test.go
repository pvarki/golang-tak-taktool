package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDetectAtakTarget(t *testing.T) {
	tests := []struct {
		name string
		info ApkInfo
		file string
		want atakTarget
	}{
		{"plugin-api", ApkInfo{Package: "com.atakmap.android.vns.plugin", TakReq: "com.atakmap.app@5.8.0.CIV", Version: "4.0 (ac524e2c) - [5.8.0]"},
			"vns.apk", atakTarget{"5.8.0", "CIV", "plugin-api"}},
		{"ATAK itself", ApkInfo{Package: "com.atakmap.app.civ", TakReq: "com.atakmap.app@5.6.0.CIV", Version: "5.6.0.24 (7f2055b0)[playstore]"},
			"x.apk", atakTarget{"5.6.0", "CIV", "plugin-api"}},
		{"ATAK without plugin-api", ApkInfo{Package: "com.atakmap.app.civ", Version: "5.7.0.25 (abc)"},
			"x.apk", atakTarget{"5.7.0", "CIV", "versionName"}},
		{"versionName", ApkInfo{Package: "com.x.plugin", Version: "1.2 (abc) - [5.7.0]"},
			"x.apk", atakTarget{"5.7.0", "", "versionName"}},
		{"file name", ApkInfo{Package: "com.x.plugin", Version: "4.2"},
			"ATAK-Plugin-vns-4.2-4bb952de-5.6.0-civ-release.apk", atakTarget{"5.6.0", "CIV", "file name"}},
		{"plugin version in file name is not taken as ATAK version", ApkInfo{Package: "com.x.plugin", Version: "4.2.1"},
			"plugin-4.2.1-release.apk", atakTarget{}},
		{"MIL from plugin-api", ApkInfo{Package: "com.x.plugin", TakReq: "com.atakmap.app@5.8.0.MIL"},
			"x.apk", atakTarget{"5.8.0", "MIL", "plugin-api"}},
		{"GOV from file name", ApkInfo{Package: "com.x.plugin", Version: "1.0"},
			"ATAK-Plugin-x-1.0-5.8.0-gov-release.apk", atakTarget{"5.8.0", "GOV", "file name"}},
		{"MIL word in file name", ApkInfo{Package: "com.x.plugin", Version: "1.0 - [5.8.0]"},
			"x-mil-release.apk", atakTarget{"5.8.0", "MIL", "versionName"}},
		{"version independent app", ApkInfo{Package: "io.opentakserver.opentakicu", Version: "1.5.4"},
			"opentak_icu_app.apk", atakTarget{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectAtakTarget(tt.info, tt.file); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCompareAndShortVersion(t *testing.T) {
	v := []string{"5.10.0", "5.8.0", "5.9.0", "4.10.1"}
	slices.SortFunc(v, compareVersions)
	if want := []string{"4.10.1", "5.8.0", "5.9.0", "5.10.0"}; !slices.Equal(v, want) {
		t.Errorf("sorted %v, want %v", v, want)
	}
	for in, want := range map[string]string{
		"5.8.0.3 (4f67063)[playstore]": "5.8.0.3",
		"4.0 (ac524e2c) - [5.8.0]":     "4.0",
		"1.5.4":                        "1.5.4",
	} {
		if got := shortVersion(in); got != want {
			t.Errorf("shortVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, configFileName)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	c, err := loadConfig(writeConfig(t, dir, "versions:\n  - 5.8.0\n  - 5.6.0\n  - 5.8.0\npackages:\n  - a.b\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Versions, []string{"5.6.0", "5.8.0"}) {
		t.Errorf("versions %v", c.Versions)
	}
	if c.Output != filepath.Join(dir, "update") {
		t.Errorf("default output %q", c.Output)
	}
	if !c.wantsPackage("a.b") || c.wantsPackage("c.d") {
		t.Error("packages filter does not work")
	}

	c, err = loadConfig(writeConfig(t, dir, "versions: [5.8.0]\noutput: ../www/update\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.Dir(dir), "www", "update"); c.Output != want {
		t.Errorf("output %q, want %q", c.Output, want)
	}
	if !c.wantsPackage("anything") {
		t.Error("without packages every package should be accepted")
	}

	for name, content := range map[string]string{
		"no versions":   "output: x\n",
		"bad version":   "versions: [5.8]\n",
		"unknown field": "versions: [5.8.0]\npackage: [a]\n",
	} {
		if _, err := loadConfig(writeConfig(t, dir, content)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestCheckOutputFolder(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "apks")
	os.Mkdir(src, 0o755)

	cfg := &Config{Dir: src}
	for name, tc := range map[string]struct {
		output string
		setup  func(out string)
		ok     bool
	}{
		"new folder":            {filepath.Join(base, "update"), func(string) {}, true},
		"empty folder":          {filepath.Join(base, "empty"), func(o string) { os.Mkdir(o, 0o755) }, true},
		"existing update":       {filepath.Join(base, "upd"), func(o string) { os.MkdirAll(filepath.Join(o, "5.8.0"), 0o755) }, true},
		"same as apk folder":    {src, func(string) {}, false},
		"parent of apk folder":  {base, func(string) {}, false},
		"unrelated full folder": {filepath.Join(base, "home"), func(o string) { os.Mkdir(o, 0o755); os.WriteFile(filepath.Join(o, "x.txt"), nil, 0o644) }, false},
	} {
		tc.setup(tc.output)
		cfg.Output = tc.output
		if err := checkOutputFolder(cfg); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, tc.ok)
		}
	}
}

func TestPlanAndExecuteCleanup(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "apks")
	out := filepath.Join(base, "update")
	os.Mkdir(src, 0o755)
	os.MkdirAll(filepath.Join(out, "5.5.0"), 0o755)
	os.MkdirAll(filepath.Join(out, "5.8.0"), 0o755)
	os.WriteFile(filepath.Join(out, "repository.inf"), []byte("5.5.0\n"), 0o644)
	os.WriteFile(filepath.Join(out, "notes.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(out, ".keep"), nil, 0o644)
	os.WriteFile(filepath.Join(out, "5.8.0", "stray.txt"), nil, 0o644)

	cfg, err := loadConfig(writeConfig(t, src, "versions: [5.8.0, 5.9.0]\noutput: ../update\n"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planRepository(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"5.5.0", "notes.txt", "repository.inf"}; !slices.Equal(plan.remove, want) {
		t.Errorf("remove %v, want %v", plan.remove, want)
	}

	var report bytes.Buffer
	printRepoReport(&report, plan, true, false)
	for _, s := range []string{"5.5.0: remove (not in versions)", "5.8.0: remove stray.txt", "5.9.0: new version folder", "5.9.0: no apks to distribute"} {
		if !strings.Contains(report.String(), s) {
			t.Errorf("report does not contain %q:\n%s", s, report.String())
		}
	}

	if err := executeRepoPlan(plan, false); err != nil {
		t.Fatal(err)
	}
	if got, want := filesIn(t, out), []string{".keep", "5.8.0", "5.9.0", "repositories.inf"}; !slices.Equal(got, want) {
		t.Errorf("update folder %v, want %v", got, want)
	}
	if got := filesIn(t, filepath.Join(out, "5.8.0")); !slices.Equal(got, []string{productInfzFilename}) {
		t.Errorf("5.8.0 contains %v", got)
	}
	data, _ := os.ReadFile(filepath.Join(out, repositoriesInfFilename))
	if string(data) != "5.8.0\n5.9.0\n" {
		t.Errorf("repositories.inf = %q", data)
	}
}

func TestIsLFSPointer(t *testing.T) {
	dir := t.TempDir()
	ptr := filepath.Join(dir, "ptr.apk")
	os.WriteFile(ptr, []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 1\n"), 0o644)
	small := filepath.Join(dir, "small.apk")
	os.WriteFile(small, []byte("PK"), 0o644)
	if lfs, _ := isLFSPointer(ptr); !lfs {
		t.Error("pointer not detected")
	}
	if lfs, _ := isLFSPointer(small); lfs {
		t.Error("small file detected as pointer")
	}
}

// writeVersionFolder writes fake apks and a matching product.infz.
func writeVersionFolder(t *testing.T, dir string, apks map[string]ApkInfo) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	var infos []ApkInfo
	for name, info := range apks {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("fake apk "+name), 0o644)
		info.ApkPath = name
		info.IconPath = strings.TrimSuffix(name, ".apk") + ".png"
		info.Hash, _ = calculateHash(p)
		st, _ := os.Stat(p)
		info.Size = int(st.Size())
		infos = append(infos, info)
	}
	f, _ := os.Create(filepath.Join(dir, productInfzFilename))
	zw := zip.NewWriter(f)
	for _, info := range infos {
		w, _ := zw.Create(info.IconPath)
		w.Write(emptyPng)
	}
	w, _ := zw.Create(productInfFilename)
	w.Write([]byte(createProductInf(infos)))
	zw.Close()
	f.Close()
}

func TestCheckRepository(t *testing.T) {
	dir := t.TempDir()
	atak := ApkInfo{Platform: "Android", Type: "app", Package: "com.atakmap.app.civ", DisplayName: "ATAK", Version: "5.8.0.3 (x)", Revision: "1", OsReq: 1}
	vns := ApkInfo{Platform: "Android", Type: "plugin", Package: "com.atakmap.android.vns.plugin", DisplayName: "VNS", Version: "4.0 (x) - [5.8.0]", Revision: "2", OsReq: 1}
	ds := ApkInfo{Platform: "Android", Type: "plugin", Package: "com.atakmap.android.datasync.plugin", DisplayName: "Data Sync", Version: "5.0.2", Revision: "3", OsReq: 1}

	writeVersionFolder(t, filepath.Join(dir, "5.8.0"), map[string]ApkInfo{"atak_app.apk": atak, "vns_plugin.apk": vns, "data_sync_plugin.apk": ds})
	writeVersionFolder(t, filepath.Join(dir, "5.9.0"), map[string]ApkInfo{"atak_app.apk": atak, "vns_plugin.apk": vns})
	os.WriteFile(filepath.Join(dir, repositoriesInfFilename), []byte("5.8.0\n5.9.0\n"), 0o644)

	r, err := checkRepository(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.errors) != 0 {
		t.Errorf("unexpected errors %v", r.errors)
	}
	if want := "5.8.0: ATAK 5.8.0.3, Data Sync 5.0.2, VNS 4.0"; r.summary[0] != want {
		t.Errorf("summary %q, want %q", r.summary[0], want)
	}
	if !slices.ContainsFunc(r.warnings, func(s string) bool { return strings.Contains(s, "5.9.0: Data Sync") && strings.Contains(s, "missing") }) {
		t.Errorf("missing Data Sync in 5.9.0 not reported: %v", r.warnings)
	}

	// Corrupt an apk, add an unlisted apk and an unlisted version folder
	os.WriteFile(filepath.Join(dir, "5.9.0", "vns_plugin.apk"), []byte("changed"), 0o644)
	os.WriteFile(filepath.Join(dir, "5.9.0", "extra.apk"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(dir, "5.5.0"), 0o755)
	cfg := &Config{Versions: []string{"5.8.0", "5.9.0"}, Packages: []string{"com.atakmap.android.vns.plugin", "com.example.missing.plugin"}}

	r, err = checkRepository(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(append(r.errors, r.warnings...), "\n")
	for _, s := range []string{
		"5.9.0/vns_plugin.apk: size",
		"5.9.0/vns_plugin.apk: sha256",
		"5.9.0/extra.apk is not listed",
		"folder 5.5.0 is not listed",
		"5.8.0: com.example.missing.plugin (com.example.missing.plugin) is missing",
	} {
		if !strings.Contains(joined, s) {
			t.Errorf("not reported: %q\n%s", s, joined)
		}
	}
	if strings.Contains(joined, "Data Sync (com.atakmap.android.datasync.plugin) is missing") {
		t.Error("with packages in taktool.yaml, only those packages should be required")
	}
	if len(r.errors) != 2 {
		t.Errorf("errors %v, want the 2 for the changed apk", r.errors)
	}
}

func TestConfigTargets(t *testing.T) {
	dir := t.TempDir()
	c, err := loadConfig(writeConfig(t, dir, `versions: [5.7.0, 5.8.0]
packages: [io.opentakserver.opentakicu, com.x.plugin, com.y.plugin]
targets:
  io.opentakserver.opentakicu: all
  com.x.plugin: 5.8.0
  com.y.plugin: [5.8.0, 5.7.0]
`))
	if err != nil {
		t.Fatal(err)
	}
	for pkg, want := range map[string][]string{
		"io.opentakserver.opentakicu": {"5.7.0", "5.8.0"},
		"com.x.plugin":                {"5.8.0"},
		"com.y.plugin":                {"5.7.0", "5.8.0"},
	} {
		if got, ok := c.targetVersions(pkg); !ok || !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", pkg, got, want)
		}
	}
	if _, ok := c.targetVersions("com.other"); ok {
		t.Error("unexpected target for a package without one")
	}

	for name, content := range map[string]string{
		"version not in versions": "versions: [5.8.0]\ntargets:\n  a.b: 5.9.0\n",
		"not in packages":         "versions: [5.8.0]\npackages: [c.d]\ntargets:\n  a.b: all\n",
		"wrong type":              "versions: [5.8.0]\ntargets:\n  a.b: {x: 1}\n",
	} {
		if _, err := loadConfig(writeConfig(t, dir, content)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRepoAsksForConfirmation(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "apks")
	out := filepath.Join(base, "update")
	os.Mkdir(src, 0o755)
	os.MkdirAll(filepath.Join(out, "5.5.0"), 0o755)
	cfgPath := writeConfig(t, src, "versions: [5.8.0]\noutput: ../update\n")
	opts := repoOptions{config: cfgPath}

	var report bytes.Buffer
	err := PackageRepository(opts, strings.NewReader("n\n"), &report)
	if !errors.Is(err, errNotConfirmed) {
		t.Fatalf("answer n: err = %v", err)
	}
	if !strings.Contains(report.String(), "-dry-run") {
		t.Error("the question should mention -dry-run")
	}
	if got := filesIn(t, out); !slices.Equal(got, []string{"5.5.0"}) {
		t.Errorf("answer n changed the update folder: %v", got)
	}

	if err := PackageRepository(opts, strings.NewReader("y\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := filesIn(t, out); !slices.Equal(got, []string{"5.8.0", "repositories.inf"}) {
		t.Errorf("after y: %v", got)
	}

	// Nothing left to do: no question
	var second bytes.Buffer
	if err := PackageRepository(opts, strings.NewReader(""), &second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.String(), "Nothing to do") {
		t.Errorf("second run:\n%s", second.String())
	}

	// -yes applies without asking
	os.MkdirAll(filepath.Join(out, "5.6.0"), 0o755)
	if err := PackageRepository(repoOptions{config: cfgPath, yes: true}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := filesIn(t, out); !slices.Equal(got, []string{"5.8.0", "repositories.inf"}) {
		t.Errorf("after -yes: %v", got)
	}
}

// fakeZip writes a zip with the given (empty) entries.
func fakeZip(t *testing.T, path string, entries ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		zw.Create(e)
	}
	zw.Close()
	f.Close()
}

func TestDetectAtakVariant(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name, version string
		entries       []string
		want          string
		warn          bool
	}{
		{"small", "5.8.0.5 (a306924)[playstore]", []string{"lib/arm64-v8a/libx.so", "assets/apks/product.inf"}, atakSmall, false},
		{"full with 32-bit libs", "5.8.0.5 (a306924)", []string{"lib/arm64-v8a/libx.so", "lib/armeabi-v7a/libx.so"}, atakFull, false},
		{"full with bundled apps", "5.8.0.5 (a306924)", []string{"assets/apks/nightvision-release.apk"}, atakFull, false},
		{"content and versionName disagree", "5.8.0.5 (a306924)", []string{"lib/arm64-v8a/libx.so"}, atakSmall, true},
	}
	for i, tt := range tests {
		p := filepath.Join(dir, "a"+string(rune('0'+i))+".apk")
		fakeZip(t, p, tt.entries...)
		got, warning, err := detectAtakVariant(p, ApkInfo{Version: tt.version})
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want || (warning != "") != tt.warn {
			t.Errorf("%s: got %q, warning %q", tt.name, got, warning)
		}
	}
}

func TestConfigAtak(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]string{
		"versions: [5.8.0]\n":             atakSmall,
		"versions: [5.8.0]\natak: Full\n": atakFull,
	} {
		c, err := loadConfig(writeConfig(t, dir, content))
		if err != nil {
			t.Fatal(err)
		}
		if c.Atak != want {
			t.Errorf("%q: atak = %q, want %q", content, c.Atak, want)
		}
	}
	if _, err := loadConfig(writeConfig(t, dir, "versions: [5.8.0]\natak: big\n")); err == nil {
		t.Error("expected an error for atak: big")
	}
}

func TestAtakVariantSelection(t *testing.T) {
	small := &repoApk{info: ApkInfo{Package: "com.atakmap.app.civ", Revision: "10"}, path: "small.apk", variant: atakSmall}
	full := &repoApk{info: ApkInfo{Package: "com.atakmap.app.civ", Revision: "10"}, path: "full.apk", variant: atakFull}
	newerFull := &repoApk{info: ApkInfo{Package: "com.atakmap.app.civ", Revision: "11"}, path: "full2.apk", variant: atakFull}
	vns := &repoApk{info: ApkInfo{Package: "com.atakmap.android.vns.plugin", Revision: "1"}, path: "vns.apk"}

	p := &repoPlan{cfg: &Config{Atak: atakSmall}}
	results := map[string]*sourceResult{}
	for _, a := range []*repoApk{small, full, newerFull, vns} {
		results[a.path] = &sourceResult{}
	}

	// Both variants in the apk folder: only the wanted one stays
	got := p.pickAtakVariant("5.8.0", []*repoApk{full, small, vns}, results)
	if len(got) != 2 || got[0] != small || got[1] != vns {
		t.Errorf("picked %v", got)
	}
	if !strings.Contains(results["full.apk"].reason, "atak: small") || results["full.apk"].removable {
		t.Errorf("full.apk result %+v", results["full.apk"])
	}

	// Only the other variant: it is kept
	if got := p.pickAtakVariant("5.7.0", []*repoApk{full}, results); len(got) != 1 {
		t.Errorf("fallback: got %v", got)
	}

	// The wanted variant wins over a newer revision of the other one
	if better, _ := p.atakPreferred(small, newerFull); !better {
		t.Error("small should be preferred over a newer full with atak: small")
	}
	if better, _ := p.atakPreferred(newerFull, small); better {
		t.Error("newer full should not replace small with atak: small")
	}
	// Same variant: the revision decides
	if better, _ := p.atakPreferred(newerFull, full); !better {
		t.Error("newer full should replace older full")
	}
}

func TestProductInfRowDiff(t *testing.T) {
	apk := ApkInfo{Type: "plugin", Package: "p", DisplayName: "VNS", Version: "4.0", Revision: "7", OsReq: 23, TakReq: "com.atakmap.app@5.8.0.CIV"}
	row := productInfRow{Type: "plugin", Package: "p", DisplayName: "VNS", Version: "4.0", Revision: "7", OsReq: "23", TakReq: "com.atakmap.app@5.8.0.CIV"}
	if d := productInfRowDiff(row, apk); d != "" {
		t.Errorf("unexpected difference: %s", d)
	}
	row.OsReq = "1"
	if d := productInfRowDiff(row, apk); !strings.Contains(d, "os requirement") {
		t.Errorf("os requirement difference not found: %q", d)
	}
}
