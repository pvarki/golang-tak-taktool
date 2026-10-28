package main

// Tests of 'taktool repo' and 'taktool check' with real (small) apks.

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type repoEnv struct {
	t        *testing.T
	src, out string
	cfgPath  string
}

func newRepoEnv(t *testing.T, config string) *repoEnv {
	t.Helper()
	resetApkCache()
	base := t.TempDir()
	e := &repoEnv{t: t, src: filepath.Join(base, "apks"), out: filepath.Join(base, "update")}
	os.Mkdir(e.src, 0o755)
	e.config(config)
	return e
}

func (e *repoEnv) config(content string) {
	e.cfgPath = writeConfig(e.t, e.src, content)
}

func (e *repoEnv) apk(name string, a testApk) {
	writeTestApk(e.t, filepath.Join(e.src, name), a)
}

// run runs taktool repo -yes and returns the report.
func (e *repoEnv) run(o repoOptions) string {
	e.t.Helper()
	o.config, o.yes = e.cfgPath, true
	var out bytes.Buffer
	if err := PackageRepository(o, strings.NewReader(""), &out); err != nil {
		e.t.Fatalf("repo: %v\n%s", err, out.String())
	}
	return out.String()
}

func (e *repoEnv) files(rel string) []string {
	e.t.Helper()
	entries, err := os.ReadDir(filepath.Join(e.out, rel))
	if err != nil {
		e.t.Fatal(err)
	}
	var names []string
	for _, x := range entries {
		names = append(names, x.Name())
	}
	return names
}

func (e *repoEnv) removed() []string {
	entries, _ := os.ReadDir(filepath.Join(e.src, removedFolderName))
	var names []string
	for _, x := range entries {
		names = append(names, x.Name())
	}
	return names
}

func vnsApk(rev int, key int) testApk {
	return testApk{pkg: "com.atakmap.android.vns.plugin", label: "VNS", versionCode: rev,
		versionName: "4." + string(rune('0'+rev)), pluginAPI: "com.atakmap.app@5.8.0.CIV", key: key}
}

func TestRealRepoMergeAndUpdate(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.7.0, 5.8.0]\noutput: ../update\n")
	e.apk("ATAK-Plugin-vns-4.1-x-5.8.0-civ-release.apk", vnsApk(1, 0))
	e.apk("icu.apk", testApk{pkg: "io.opentakserver.opentakicu", label: "OpenTAK ICU", versionCode: 10})
	e.apk("wave57.apk", testApk{pkg: "com.atakmap.android.wave.plugin", label: "WAVE", versionCode: 3, pluginAPI: "com.atakmap.app@5.7.0.CIV"})
	report := e.run(repoOptions{})
	if got := e.files("5.8.0"); !slices.Equal(got, []string{"opentak_icu_app.apk", productInfzFilename, "vns_plugin.apk"}) {
		t.Errorf("5.8.0: %v\n%s", got, report)
	}
	if got := e.files("5.7.0"); !slices.Equal(got, []string{"opentak_icu_app.apk", productInfzFilename, "wave_plugin.apk"}) {
		t.Errorf("5.7.0: %v", got)
	}
	if entries, _ := os.ReadDir(e.src); len(entries) != 1 {
		t.Errorf("apk folder should only have taktool.yaml left: %v", entries)
	}

	// A second run changes nothing
	if r := e.run(repoOptions{dryRun: true}); !strings.Contains(r, "Changes: none") {
		t.Errorf("second run:\n%s", r)
	}

	// A newer revision replaces the old one, which goes to removed/; an
	// older one stays in the apk folder; a newer one signed with another
	// key gives a warning
	e.apk("vns-new.apk", vnsApk(2, 1))
	e.apk("vns-old.apk", vnsApk(0, 0))
	report = e.run(repoOptions{})
	if !strings.Contains(report, "VNS 4.1 -> 4.2") || !strings.Contains(report, "different certificate") {
		t.Errorf("update report:\n%s", report)
	}
	if got := e.removed(); !slices.Equal(got, []string{"5.8.0-vns_plugin.apk"}) {
		t.Errorf("removed: %v", got)
	}
	if _, err := os.Stat(filepath.Join(e.src, "vns-old.apk")); err != nil {
		t.Error("older apk should stay in the apk folder")
	}
	if info, err := readApkData(filepath.Join(e.out, "5.8.0", "vns_plugin.apk")); err != nil || info.Revision != "2" {
		t.Errorf("5.8.0 has %+v %v", info, err)
	}

	// -clean keeps the older apk of the apk folder (the apk in the update
	// folder may be another build; it is shown in the report) and the
	// replaced one in removed/, because the new one is signed with another
	// certificate; -cleanall removes both
	report = e.run(repoOptions{clean: true})
	if _, err := os.Stat(filepath.Join(e.src, "vns-old.apk")); err != nil {
		t.Errorf("-clean removed the older apk:\n%s", report)
	}
	if !strings.Contains(report, "vns-old.apk: older") {
		t.Errorf("older apk not shown:\n%s", report)
	}
	if got := e.removed(); len(got) != 1 {
		t.Errorf("-clean removed an apk replaced by one with another certificate: %v", got)
	}
	report = e.run(repoOptions{cleanAll: true})
	if !strings.Contains(report, "  5.8.0-vns_plugin.apk\n") {
		t.Errorf("-cleanall does not name what it removes:\n%s", report)
	}
	if _, err := os.Stat(filepath.Join(e.src, "vns-old.apk")); !os.IsNotExist(err) {
		t.Error("-cleanall left the older apk")
	}
	if got := e.removed(); len(got) != 0 {
		t.Errorf("-cleanall left in removed/: %v", got)
	}

	// check passes
	r, err := checkRepository(e.out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.errors) != 0 {
		t.Errorf("check errors: %v", r.errors)
	}
}

func TestRealRepoConfigChangesMoveToRemoved(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.7.0, 5.8.0]\noutput: ../update\n")
	e.apk("icu.apk", testApk{pkg: "io.opentakserver.opentakicu", label: "OpenTAK ICU", versionCode: 10})
	e.apk("vns.apk", vnsApk(1, 0))
	e.run(repoOptions{})

	// targets narrowed to 5.8.0, VNS excluded
	e.config("versions: [5.7.0, 5.8.0]\noutput: ../update\nexclude: [com.atakmap.android.vns.plugin]\n" +
		"targets:\n  io.opentakserver.opentakicu: [5.8.0]\n")
	report := e.run(repoOptions{})
	if !strings.Contains(report, "not in targets") || !strings.Contains(report, "excluded") {
		t.Errorf("report:\n%s", report)
	}
	if got := e.files("5.7.0"); !slices.Equal(got, []string{productInfzFilename}) {
		t.Errorf("5.7.0: %v", got)
	}
	if got := e.files("5.8.0"); !slices.Equal(got, []string{"opentak_icu_app.apk", productInfzFilename}) {
		t.Errorf("5.8.0: %v", got)
	}
	if got := e.removed(); !slices.Equal(got, []string{"5.7.0-opentak_icu_app.apk", "5.8.0-vns_plugin.apk"}) {
		t.Errorf("removed: %v", got)
	}

	// A version removed from versions: its apks go to removed/ too
	e.config("versions: [5.7.0]\noutput: ../update\n")
	e.run(repoOptions{})
	if got := e.removed(); !slices.Contains(got, "5.8.0-opentak_icu_app.apk") {
		t.Errorf("removed after dropping 5.8.0: %v", got)
	}

	// -cleanall empties removed/
	e.run(repoOptions{cleanAll: true})
	if _, err := os.Stat(filepath.Join(e.src, removedFolderName)); !os.IsNotExist(err) {
		t.Errorf("removed/ left: %v", e.removed())
	}
}

func TestRealRepoRestoresInterruptedSwap(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.8.0]\noutput: ../update\n")
	e.apk("vns.apk", vnsApk(1, 0))
	e.run(repoOptions{})
	// As if a run stopped after renaming 5.8.0 to .5.8.0.old
	if err := os.Rename(filepath.Join(e.out, "5.8.0"), filepath.Join(e.out, ".5.8.0.old")); err != nil {
		t.Fatal(err)
	}
	report := e.run(repoOptions{})
	if !strings.Contains(report, "restored") {
		t.Errorf("report:\n%s", report)
	}
	if got := e.files("5.8.0"); !slices.Equal(got, []string{productInfzFilename, "vns_plugin.apk"}) {
		t.Errorf("5.8.0: %v", got)
	}
	if got := e.removed(); len(got) != 0 {
		t.Errorf("removed: %v", got)
	}
	if _, err := os.Stat(filepath.Join(e.out, ".5.8.0.old")); !os.IsNotExist(err) {
		t.Error(".old left")
	}
}

func TestRealRepoUnreadableApkStopsRun(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.8.0]\noutput: ../update\n")
	e.apk("vns.apk", vnsApk(1, 0))
	e.run(repoOptions{})
	bad := filepath.Join(e.out, "5.8.0", "vns_plugin.apk")
	os.WriteFile(bad, []byte("not an apk"), 0o644)
	var out bytes.Buffer
	err := PackageRepository(repoOptions{config: e.cfgPath, yes: true}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(bad); string(b) != "not an apk" {
		t.Error("the unreadable apk was changed")
	}
}

func TestRealCheckFindsProblems(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.7.0, 5.8.0]\noutput: ../update\n")
	e.apk("vns.apk", vnsApk(1, 0))
	e.apk("icu.apk", testApk{pkg: "io.opentakserver.opentakicu", label: "OpenTAK ICU", versionCode: 10})
	e.run(repoOptions{})

	// A plugin copied by hand into the wrong version
	src := filepath.Join(e.out, "5.8.0", "vns_plugin.apk")
	dst := filepath.Join(e.out, "5.7.0", "vns_plugin.apk")
	in, _ := os.Open(src)
	o, _ := os.Create(dst)
	io.Copy(o, in)
	in.Close()
	o.Close()
	cfg, _ := loadConfig(e.cfgPath)
	vp := &versionPlan{version: "5.7.0", apks: map[string]*repoApk{}}
	for _, n := range []string{"vns_plugin.apk", "opentak_icu_app.apk"} {
		info, err := readApkData(filepath.Join(e.out, "5.7.0", n))
		if err != nil {
			t.Fatal(err)
		}
		vp.apks[info.Package] = &repoApk{info: info, path: filepath.Join(e.out, "5.7.0", n)}
	}
	if err := writeProductInfz(filepath.Join(e.out, "5.7.0"), vp.productInfos(), "", io.Discard); err != nil {
		t.Fatal(err)
	}
	r, err := checkRepository(e.out, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(r.warnings, func(s string) bool { return strings.Contains(s, "5.7.0: VNS is made for ATAK 5.8.0") }) {
		t.Errorf("warnings %v", r.warnings)
	}
	// and 'taktool repo' takes it out again
	report := e.run(repoOptions{})
	if !strings.Contains(report, "made for ATAK 5.8.0") {
		t.Errorf("report:\n%s", report)
	}
}

func TestRealRepoAtakVariant(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.8.0]\noutput: ../update\n")
	atak := func(full bool) testApk {
		a := testApk{pkg: "com.atakmap.app.civ", label: "ATAK", versionCode: 50, versionName: "5.8.0.5 (a306924)[playstore]",
			pluginAPI: "com.atakmap.app@5.8.0.CIV"}
		if full {
			a.versionName = "5.8.0.5 (a306924)"
			a.extra = map[string]string{"lib/armeabi-v7a/libx.so": "x"}
		}
		return a
	}
	e.apk("ATAK-5.8.0.5-civSmall-release.apk", atak(false))
	e.apk("ATAK-5.8.0.5-civ-release.apk", atak(true))
	report := e.run(repoOptions{})
	if !strings.Contains(report, "ATAK 5.8.0.5 (small)") {
		t.Errorf("report:\n%s", report)
	}
	if _, err := os.Stat(filepath.Join(e.src, "ATAK-5.8.0.5-civ-release.apk")); err != nil {
		t.Error("the full ATAK should stay in the apk folder")
	}
	// Switching to full replaces it; the small one goes to removed/
	e.config("versions: [5.8.0]\noutput: ../update\natak: full\n")
	e.run(repoOptions{})
	if got := e.removed(); !slices.Equal(got, []string{"5.8.0-atak_app.apk"}) {
		t.Errorf("removed: %v", got)
	}
}

// A .old folder left by a run that stopped after the swap: the replaced
// revision has the same file name as its replacement, but must not be lost.
func TestRealLeftoverOldFolderKeepsReplacedRevision(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.8.0]\noutput: ../update\n")
	e.apk("vns.apk", vnsApk(1, 0))
	e.run(repoOptions{})
	old := filepath.Join(e.out, ".5.8.0.old")
	os.Mkdir(old, 0o755)
	writeTestApk(t, filepath.Join(old, "vns_plugin.apk"), vnsApk(0, 0)) // the replaced revision
	if err := os.Link(filepath.Join(e.out, "5.8.0", productInfzFilename), filepath.Join(old, productInfzFilename)); err != nil {
		t.Skip("no hard links")
	}

	report := e.run(repoOptions{})
	if !strings.Contains(report, ".5.8.0.old") {
		t.Errorf("leftover not reported:\n%s", report)
	}
	if got := e.removed(); !slices.Equal(got, []string{"5.8.0-vns_plugin.apk"}) {
		t.Errorf("the replaced revision was lost: removed %v", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error(".old left")
	}
}

// -clean keeps an apk in removed/ that has the same revision as a
// distributed one but other content (a build for another ATAK version).
func TestRealCleanKeepsOtherBuildOfSameRevision(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.7.0, 5.8.0]\noutput: ../update\n")
	vns57 := vnsApk(1, 0)
	vns57.pluginAPI = "com.atakmap.app@5.7.0.CIV"
	e.apk("vns57.apk", vns57)
	e.apk("vns58.apk", vnsApk(1, 0))
	e.run(repoOptions{})
	e.config("versions: [5.8.0]\noutput: ../update\n")
	e.run(repoOptions{clean: true})
	if got := e.removed(); !slices.Equal(got, []string{"5.7.0-vns_plugin.apk"}) {
		t.Errorf("removed: %v", got)
	}
}

// Loose apks and other folders in the update folder go to removed/.
func TestRealLooseApksInUpdateFolder(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.8.0]\noutput: ../update\n")
	e.apk("vns.apk", vnsApk(1, 0))
	e.run(repoOptions{})
	writeTestApk(t, filepath.Join(e.out, "loose.apk"), vnsApk(5, 0))
	os.Mkdir(filepath.Join(e.out, "5.8"), 0o755)
	writeTestApk(t, filepath.Join(e.out, "5.8", "x.apk"), vnsApk(6, 0))
	os.WriteFile(filepath.Join(e.out, "notes.txt"), nil, 0o644)
	e.run(repoOptions{})
	if got := e.removed(); !slices.Equal(got, []string{"5.8-x.apk", "update-loose.apk"}) {
		t.Errorf("removed: %v", got)
	}
	if got := e.files(""); !slices.Equal(got, []string{"5.8.0", repositoriesInfFilename}) {
		t.Errorf("update folder: %v", got)
	}
}

// The cache on disk: a renamed apk is not read again, a changed one is.
func TestRealApkCacheOnDisk(t *testing.T) {
	t.Setenv("TAKTOOL_CACHE", t.TempDir())
	resetApkCache()
	defer resetApkCache()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.apk")
	writeTestApk(t, a, vnsApk(1, 0))
	if _, fresh, err := cachedApkData(a); err != nil || !fresh {
		t.Fatalf("first read: fresh %v, %v", fresh, err)
	}
	saveApkCache()
	resetApkCache()

	b := filepath.Join(dir, "renamed.apk")
	os.Rename(a, b)
	info, fresh, err := cachedApkData(b)
	if err != nil || fresh || info.ApkPath != b || info.Revision != "1" {
		t.Errorf("renamed: fresh %v, %+v, %v", fresh, info, err)
	}
	writeTestApk(t, b, vnsApk(2, 0))
	if info, fresh, _ := cachedApkData(b); !fresh || info.Revision != "2" {
		t.Errorf("changed: fresh %v, revision %s", fresh, info.Revision)
	}
}

// A vector icon with a gradient is drawn without it, and the report says so.
func TestRealPartlyDrawnIconIsReported(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.8.0]\noutput: ../update\n")
	a := vnsApk(1, 0)
	a.vectorIcon = []vectorPath{
		{"fillColor": "-1", "pathData": "M2,2h20v20h-20z"},
		{"fillColor": "res/color/gradient.xml", "pathData": "M2,2L22,22L2,22z"},
	}
	e.apk("vns.apk", a)
	report := e.run(repoOptions{verbose: true})
	if !strings.Contains(report, "VNS: gradients or colours that could not be resolved not drawn; to replace it add images/vns.png") {
		t.Errorf("report:\n%s", report)
	}
	if short := e.run(repoOptions{dryRun: true}); !strings.Contains(short, "1 partly drawn") {
		t.Errorf("short report:\n%s", short)
	}
}

// Apks for ATAK versions that are not distributed are counted per version on
// one line without -v; images no app uses are reported.
func TestRealReportSummarisesOtherAtakVersions(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.8.0]\noutput: ../update\n")
	e.apk("vns.apk", vnsApk(1, 0))
	for i, v := range []string{"4.10.0", "4.10.0", "5.5.0"} {
		a := testApk{pkg: fmt.Sprintf("com.example.p%d.plugin", i), label: fmt.Sprintf("P%d", i), versionCode: 1,
			pluginAPI: "com.atakmap.app@" + v + ".CIV"}
		e.apk(fmt.Sprintf("p%d.apk", i), a)
	}
	os.Mkdir(filepath.Join(e.src, "images"), 0o755)
	os.WriteFile(filepath.Join(e.src, "images", "darkmode.png"), testPNG(color.NRGBA{1, 2, 3, 255}), 0o644)

	short := e.run(repoOptions{dryRun: true})
	if !strings.Contains(short, "3 for ATAK versions not in versions (4.10.0: 2, 5.5.0: 1)") {
		t.Errorf("short report:\n%s", short)
	}
	if strings.Contains(short, "p0.apk") {
		t.Errorf("short report lists the apks:\n%s", short)
	}
	if !strings.Contains(short, "1 images/ files unused") {
		t.Errorf("unused image not reported:\n%s", short)
	}
	long := e.run(repoOptions{dryRun: true, verbose: true})
	if !strings.Contains(long, "p0.apk: made for ATAK 4.10.0") || !strings.Contains(long, "images/darkmode.png") {
		t.Errorf("verbose report:\n%s", long)
	}
}

// A plugin that still uses the plugin template's package name is not
// distributed, and one already in the update folder is taken out.
func TestRealTemplatePackageIsRejected(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.8.0]\noutput: ../update\n")
	tmpl := testApk{pkg: "com.atakmap.android.plugintemplate.plugin", label: "APRS Import", versionCode: 1,
		pluginAPI: "com.atakmap.app@5.8.0.CIV"}
	e.apk("aprs.apk", tmpl)
	e.apk("vns.apk", vnsApk(1, 0))
	report := e.run(repoOptions{})
	if !strings.Contains(report, "Not distributed, they use the plugin template's package name:") || !strings.Contains(report, "APRS Import 1.0") {
		t.Errorf("report:\n%s", report)
	}
	if got := e.files("5.8.0"); !slices.Equal(got, []string{productInfzFilename, "vns_plugin.apk"}) {
		t.Errorf("5.8.0: %v", got)
	}
	if _, err := os.Stat(filepath.Join(e.src, "aprs.apk")); err != nil {
		t.Error("the template apk should stay in the apk folder")
	}

	// Put in by hand (or by an older taktool): taken out to removed/
	dir := filepath.Join(e.out, "5.8.0")
	writeTestApk(t, filepath.Join(dir, "aprs_import_plugin.apk"), tmpl)
	vp := &versionPlan{version: "5.8.0", apks: map[string]*repoApk{}}
	for _, n := range []string{"vns_plugin.apk", "aprs_import_plugin.apk"} {
		info, err := readApkData(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		vp.apks[info.Package] = &repoApk{info: info, path: filepath.Join(dir, n)}
	}
	if err := writeProductInfz(dir, vp.productInfos(), "", io.Discard); err != nil {
		t.Fatal(err)
	}
	r, _ := checkRepository(e.out, nil)
	if !slices.ContainsFunc(r.warnings, func(s string) bool { return strings.Contains(s, "plugin template") }) {
		t.Errorf("check warnings %v", r.warnings)
	}
	e.run(repoOptions{})
	if got := e.removed(); !slices.Equal(got, []string{"5.8.0-aprs_import_plugin.apk"}) {
		t.Errorf("removed: %v", got)
	}
}

func vns57Apk(rev int) testApk {
	a := vnsApk(rev, 0)
	a.pluginAPI = "com.atakmap.app@5.7.0.CIV"
	return a
}

func TestRealCleanRemovedFolderPerAtakVersion(t *testing.T) {
	cfg := "versions: [5.7.0, 5.8.0]\noutput: ../update\n"
	e := newRepoEnv(t, cfg)
	e.apk("vns57.apk", vns57Apk(1))
	e.apk("vns58.apk", vnsApk(2, 0))
	e.run(repoOptions{})

	// A wrong targets entry takes VNS out of 5.7.0. -clean keeps it in
	// removed/: 5.8.0 has a newer revision, but for another ATAK version
	e.config(cfg + "targets:\n  com.atakmap.android.vns.plugin: [5.8.0]\n")
	e.run(repoOptions{clean: true})
	e.run(repoOptions{clean: true})
	if got := e.removed(); !slices.Equal(got, []string{"5.7.0-vns_plugin.apk"}) {
		t.Fatalf("removed: %v", got)
	}

	// Getting it back: move it into the apk folder, fix the config, run
	os.Rename(filepath.Join(e.src, removedFolderName, "5.7.0-vns_plugin.apk"), filepath.Join(e.src, "vns57.apk"))
	e.config(cfg)
	e.run(repoOptions{})
	if info, err := readApkData(filepath.Join(e.out, "5.7.0", "vns_plugin.apk")); err != nil || info.Revision != "1" {
		t.Fatalf("5.7.0 has %+v %v", info, err)
	}

	// A newer VNS for 5.7.0 replaces it. The replaced one is moved to
	// removed/ and not deleted by -clean in the same run (the question and
	// the report did not count it), only by the next run
	e.apk("vns57-new.apk", vns57Apk(3))
	report := e.run(repoOptions{clean: true})
	if !strings.Contains(report, "not removed in this run") {
		t.Errorf("report:\n%s", report)
	}
	if got := e.removed(); !slices.Equal(got, []string{"5.7.0-vns_plugin.apk"}) {
		t.Fatalf("removed after the replacing run: %v", got)
	}
	// The next -clean names it with the reason before removing it
	report = e.run(repoOptions{clean: true})
	if !strings.Contains(report, "5.7.0-vns_plugin.apk: VNS 4.1 (revision 1), replaced by VNS 4.3 (revision 3) in 5.7.0") {
		t.Errorf("report:\n%s", report)
	}
	if got := e.removed(); len(got) != 0 {
		t.Errorf("removed after the next -clean: %v", got)
	}
}

func TestRealCleanKeepsPackageWithConflict(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.8.0]\noutput: ../update\n")
	e.apk("vns1.apk", vnsApk(1, 0))
	e.run(repoOptions{})
	// Two different builds of revision 3 and an older one: nothing new is
	// distributed, and -clean leaves the older apks alone (also in removed/)
	os.MkdirAll(filepath.Join(e.src, removedFolderName), 0o755)
	writeTestApk(t, filepath.Join(e.src, removedFolderName, "5.8.0-vns_plugin.apk"), vnsApk(0, 0))
	e.apk("vns3a.apk", vnsApk(3, 0))
	e.apk("vns3b.apk", vnsApk(3, 1))
	e.apk("vns2.apk", vnsApk(2, 0))
	report := e.run(repoOptions{clean: true})
	if _, err := os.Stat(filepath.Join(e.src, "vns2.apk")); err != nil {
		t.Errorf("-clean removed the older apk while revision 3 has a conflict:\n%s", report)
	}
	if !strings.Contains(report, "kept until the conflict") {
		t.Errorf("report:\n%s", report)
	}
	if got := e.removed(); len(got) != 1 {
		t.Errorf("removed: %v", got)
	}
	// -cleanall removes them
	e.run(repoOptions{cleanAll: true})
	if _, err := os.Stat(filepath.Join(e.src, "vns2.apk")); !os.IsNotExist(err) {
		t.Error("-cleanall left the older apk")
	}
}

func TestRealExcludedFileDistributedAnyway(t *testing.T) {
	cfg := "versions: [5.8.0]\noutput: ../update\n"
	e := newRepoEnv(t, cfg)
	legacy := testApk{pkg: "com.atakmap.android.wave.plugin", label: "WAVE", versionCode: 5,
		versionName: "2.0-legacy", pluginAPI: "com.atakmap.app@5.8.0.CIV"}
	e.apk("ATAK-Plugin-wave-legacy-2.0-5.8.0-civ-release.apk", legacy)
	e.run(repoOptions{})
	// The same apk again in the apk folder, now excluded by file name, and
	// the normal WAVE with a lower revision
	e.apk("ATAK-Plugin-wave-legacy-2.0-5.8.0-civ-release.apk", legacy)
	normal := legacy
	normal.versionCode, normal.versionName = 3, "2.1"
	e.apk("ATAK-Plugin-wave-2.1-5.8.0-civ-release.apk", normal)
	e.config(cfg + "exclude: [\"*wave-legacy*\"]\n")
	report := e.run(repoOptions{clean: true})
	if !strings.Contains(report, "excluded by its file name") {
		t.Errorf("no warning:\n%s", report)
	}
	if _, err := os.Stat(filepath.Join(e.src, "ATAK-Plugin-wave-2.1-5.8.0-civ-release.apk")); err != nil {
		t.Errorf("-clean removed the normal WAVE:\n%s", report)
	}
}

func TestRealCleanKeepsOlderRevisionsOfTheApkFolder(t *testing.T) {
	// WAVE Legacy has a higher revision and is distributed by mistake (no
	// exclude): -clean does not remove the normal WAVE, only -cleanall would
	e := newRepoEnv(t, "versions: [5.8.0]\noutput: ../update\n")
	legacy := testApk{pkg: "com.atakmap.android.wave.plugin", label: "WAVE", versionCode: 5,
		versionName: "2.0-legacy", pluginAPI: "com.atakmap.app@5.8.0.CIV"}
	normal := legacy
	normal.versionCode, normal.versionName = 3, "2.1"
	e.apk("wave-legacy.apk", legacy)
	e.apk("wave.apk", normal)
	e.run(repoOptions{clean: true})
	e.run(repoOptions{clean: true})
	if _, err := os.Stat(filepath.Join(e.src, "wave.apk")); err != nil {
		t.Error("-clean removed the older revision")
	}
}

func TestRealDefaultOutputWarnsAboutOldUpdateFolder(t *testing.T) {
	e := newRepoEnv(t, "versions: [5.8.0]\n")
	os.MkdirAll(filepath.Join(e.src, "update"), 0o755)
	os.WriteFile(filepath.Join(e.src, "update", repositoriesInfFilename), []byte("5.8.0\n"), 0o644)
	e.apk("vns.apk", vnsApk(1, 0))
	if r := e.run(repoOptions{dryRun: true}); !strings.Contains(r, "has no output") {
		t.Errorf("no warning:\n%s", r)
	}
}
