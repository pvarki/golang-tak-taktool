package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMoveToRemoved(t *testing.T) {
	dir := t.TempDir()
	removed := filepath.Join(dir, "removed")
	a := filepath.Join(dir, "a.apk")
	os.WriteFile(a, []byte("one"), 0o644)
	if _, err := moveToRemoved(a, removed, "5.6.0"); err != nil {
		t.Fatal(err)
	}
	// Another file with the same name gets a number
	os.WriteFile(a, []byte("two"), 0o644)
	if _, err := moveToRemoved(a, removed, "5.6.0"); err != nil {
		t.Fatal(err)
	}
	// A hard link of a file that is already there is only removed
	b := filepath.Join(dir, "b.apk")
	if err := os.Link(filepath.Join(removed, "5.6.0-a.apk"), b); err != nil {
		t.Skip("no hard links:", err)
	}
	os.Rename(b, filepath.Join(dir, "a.apk"))
	if _, err := moveToRemoved(filepath.Join(dir, "a.apk"), removed, "5.6.0"); err != nil {
		t.Fatal(err)
	}
	if got := filesIn(t, removed); !slices.Equal(got, []string{"5.6.0-a-1.apk", "5.6.0-a.apk"}) {
		t.Errorf("removed folder: %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.apk")); !os.IsNotExist(err) {
		t.Error("source not removed")
	}
}

func TestRetireFolder(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, ".5.8.0.old")
	removed := filepath.Join(dir, "removed")
	os.Mkdir(old, 0o755)
	os.Mkdir(filepath.Join(old, "backup"), 0o755)
	for _, n := range []string{"keep.apk", "drop.apk", productInfzFilename, "notes.txt", filepath.Join("backup", "sub.apk"), filepath.Join("backup", "keep.apk")} {
		os.WriteFile(filepath.Join(old, n), []byte(n), 0o644)
	}
	if err := retireFolder(old, "5.8.0", removed, keepNames(map[string]bool{"keep.apk": true})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("folder not removed")
	}
	// Apks in subfolders are moved too (only the top-level keep.apk is in the new folder)
	if got := filesIn(t, removed); !slices.Equal(got, []string{"5.8.0-drop.apk", "5.8.0-keep.apk", "5.8.0-sub.apk"}) {
		t.Errorf("removed folder: %v", got)
	}
}

func TestLeftoverOldFolderIsKeptWithoutVersionFolder(t *testing.T) {
	base := t.TempDir()
	cfg := &Config{Dir: filepath.Join(base, "apks"), Output: filepath.Join(base, "update"), Versions: []string{"5.8.0"}}
	old := filepath.Join(cfg.Output, ".5.8.0.old")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "vns_plugin.apk"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(cfg.Output, ".5.7.0.tmp"), 0o755)

	notes := cleanupLeftoverFolders(cfg, removedDir(cfg))
	if _, err := os.Stat(filepath.Join(old, "vns_plugin.apk")); err != nil {
		t.Error("the only copy of 5.8.0 was removed")
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "restores it") {
		t.Errorf("notes %v", notes)
	}
	if _, err := os.Stat(filepath.Join(cfg.Output, ".5.7.0.tmp")); !os.IsNotExist(err) {
		t.Error(".tmp folder not removed")
	}

	// With the version folder back, the .old folder is cleaned up
	os.MkdirAll(filepath.Join(cfg.Output, "5.8.0"), 0o755)
	cleanupLeftoverFolders(cfg, removedDir(cfg))
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error(".old folder not removed")
	}
	if got := filesIn(t, removedDir(cfg)); !slices.Equal(got, []string{"5.8.0-vns_plugin.apk"}) {
		t.Errorf("removed folder: %v", got)
	}
}

func TestPlanRestoresInterruptedVersionFolder(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "apks")
	out := filepath.Join(base, "update")
	os.Mkdir(src, 0o755)
	old := filepath.Join(out, ".5.8.0.old")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(out, repositoriesInfFilename), []byte("5.8.0\n"), 0o644)
	os.WriteFile(filepath.Join(old, productInfzFilename), nil, 0o644)
	cfg, err := loadConfig(writeConfig(t, src, "versions: [5.8.0]\noutput: ../update\n"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := planRepository(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !p.versions[0].restoring || !p.versions[0].hasChanges() {
		t.Fatalf("not restoring: %+v", p.versions[0])
	}
	if _, err := executeRepoPlan(p, cleanNone, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "5.8.0", productInfzFilename)); err != nil {
		t.Error("5.8.0 not rebuilt")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error(".old folder left")
	}
}

func TestNewestPerPackageConflictOnlyAtTop(t *testing.T) {
	mk := func(path, rev, hash string) *repoApk {
		return &repoApk{path: path, info: ApkInfo{Package: "p", Revision: rev, Hash: hash, DisplayName: "P"}}
	}
	list := []*repoApk{mk("a", "10", "x"), mk("b", "10", "y"), mk("c", "11", "z"), mk("d", "11", "z")}
	results := map[string]*sourceResult{}
	for _, a := range list {
		results[a.path] = &sourceResult{}
	}
	p := &repoPlan{cfg: &Config{}}
	out := p.newestPerPackage("5.8.0", list, results)
	if len(out) != 1 || out[0].path != "c" {
		t.Fatalf("kept %v", out)
	}
	if !results["d"].removable || !strings.Contains(results["d"].reason, "copy of c") {
		t.Errorf("d: %+v", results["d"])
	}
	if !strings.Contains(results["a"].reason, "older") || len(p.warnings) != 0 {
		t.Errorf("a: %+v, warnings %v", results["a"], p.warnings)
	}

	// Two different apks at the highest revision: none is distributed
	list = append(list, mk("e", "11", "w"))
	results["e"] = &sourceResult{}
	for _, r := range results {
		*r = sourceResult{}
	}
	if out := p.newestPerPackage("5.8.0", list, results); len(out) != 0 || len(p.warnings) != 1 {
		t.Errorf("kept %v, warnings %v", out, p.warnings)
	}
	// ... and the older revisions are kept until that is resolved
	if results["a"].removable || !strings.Contains(results["a"].reason, "conflict") || !p.conflicts["5.8.0"]["p"] {
		t.Errorf("a: %+v, conflicts %v", results["a"], p.conflicts)
	}
}

func TestLeftoverOldFolderOfDroppedVersion(t *testing.T) {
	base := t.TempDir()
	cfg := &Config{Dir: filepath.Join(base, "apks"), Output: filepath.Join(base, "update"), Versions: []string{"5.8.0"}}
	old := filepath.Join(cfg.Output, ".5.6.0.old")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "vns_plugin.apk"), []byte("x"), 0o644)
	if notes := cleanupLeftoverFolders(cfg, removedDir(cfg)); len(notes) != 0 {
		t.Errorf("notes %v", notes)
	}
	if got := filesIn(t, removedDir(cfg)); !slices.Equal(got, []string{"5.6.0-vns_plugin.apk"}) {
		t.Errorf("removed folder: %v", got)
	}
}
