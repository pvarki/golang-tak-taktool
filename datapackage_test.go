package main

import (
	"archive/zip"
	"encoding/xml"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type parsedManifest struct {
	Params []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"Configuration>Parameter"`
	Contents []struct {
		ZipEntry string `xml:"zipEntry,attr"`
	} `xml:"Contents>Content"`
}

func TestBuildManifestEscapesXML(t *testing.T) {
	m := Manifest{
		UID:          "uid",
		Name:         `R&D "x" <1>`,
		FileContents: []string{"a&b.txt", "dir/<c>.kml"},
	}
	s, err := buildManifest(m)
	if err != nil {
		t.Fatal(err)
	}

	var p parsedManifest
	if err := xml.Unmarshal([]byte(s), &p); err != nil {
		t.Fatalf("manifest is not valid XML: %v\n%s", err, s)
	}
	if p.Params[1].Name != "name" || p.Params[1].Value != m.Name {
		t.Errorf("name parameter = %+v, want %q", p.Params[1], m.Name)
	}
	for i, c := range p.Contents {
		if c.ZipEntry != m.FileContents[i] {
			t.Errorf("content %d = %q, want %q", i, c.ZipEntry, m.FileContents[i])
		}
	}
}

func zipNames(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func TestPackageDataPackageDoesNotIncludeItself(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.MkdirAll(filepath.Join("sub", "deeper"), 0o755)
	os.WriteFile("a.txt", []byte("a"), 0o644)
	os.WriteFile(filepath.Join("sub", "deeper", "b.kml"), []byte("b"), 0o644)

	// Run twice: the second run must not pack the first run's package
	for range 2 {
		if err := PackageDataPackage("", "pkg", "dpk", false, false); err != nil {
			t.Fatal(err)
		}
	}

	got := zipNames(t, "pkg.dpk")
	want := []string{"MANIFEST/manifest.xml", "a.txt", "sub/deeper/b.kml"}
	if !slices.Equal(got, want) {
		t.Errorf("zip contents = %v, want %v", got, want)
	}
	if _, err := os.Stat("pkg.dpk.tmp"); !os.IsNotExist(err) {
		t.Error("temporary file was left behind")
	}
}

func TestPackageDataPackageDefaultNameAndExtension(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Mission 1")
	os.Mkdir(dir, 0o755)
	t.Chdir(dir)
	os.WriteFile("a.txt", []byte("a"), 0o644)

	if err := PackageDataPackage("", "", ".zip", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("Mission 1.zip"); err != nil {
		t.Errorf("expected package named after the directory: %v", err)
	}
}

func TestPackageDataPackageRejectsBadUID(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := PackageDataPackage("not-a-uuid", "x", "dpk", false, false); err == nil {
		t.Error("expected an error for an invalid UID")
	}
}

func TestCollectFilesSkipsHiddenAndOldPackages(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"a.kml", "old.dpk", "sub/b.dpk", ".git/config", ".DS_Store", "sub/Thumbs.db", "sub/c.txt", "new.dpk"} {
		p := filepath.Join(root, filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(f), 0o644)
	}
	// old.dpk is a data package of an earlier run; plain.dpk is not
	f, _ := os.Create(filepath.Join(root, "old.dpk"))
	zw := zip.NewWriter(f)
	zw.Create("MANIFEST/manifest.xml")
	zw.Close()
	f.Close()
	os.WriteFile(filepath.Join(root, "plain.dpk"), []byte("x"), 0o644)
	got, err := collectFiles(root, ".dpk", "new.dpk")
	if err != nil {
		t.Fatal(err)
	}
	// Data packages only in the root folder are left out; sub/b.dpk is content
	if want := []string{"a.kml", "plain.dpk", "sub/b.dpk", "sub/c.txt"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
