package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileKey(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.apk")
	os.WriteFile(a, make([]byte, 300<<10), 0o644)
	k1, err := fileKey(a)
	if err != nil {
		t.Fatal(err)
	}
	// A hard link or a renamed file has the same key
	b := filepath.Join(dir, "b.apk")
	if os.Link(a, b) == nil {
		if k2, _ := fileKey(b); k2 != k1 {
			t.Error("hard link has another key")
		}
	}
	// Changed content (same size, same time) has another key
	data := make([]byte, 300<<10)
	data[len(data)-1] = 1
	st, _ := os.Stat(a)
	os.Remove(b)
	os.WriteFile(a, data, 0o644)
	os.Chtimes(a, st.ModTime(), st.ModTime())
	if k3, _ := fileKey(a); k3 == k1 {
		t.Error("changed file has the same key")
	}
}

func TestApkCacheSaveAndLoad(t *testing.T) {
	t.Setenv("TAKTOOL_CACHE", t.TempDir())
	resetApkCache()
	defer resetApkCache()

	loadApkCache()
	apkCache.entries["k"] = cachedApk{Info: ApkInfo{Package: "p", Hash: "h"}, LastUsed: time.Now().Format(time.DateOnly)}
	apkCache.entries["old"] = cachedApk{Info: ApkInfo{Package: "q"}, LastUsed: "2000-01-01"}
	apkCache.dirty = true
	saveApkCache()

	resetApkCache()
	loadApkCache()
	got, ok := apkCache.entries["k"]
	_, old := apkCache.entries["old"]
	if !ok || got.Info.Hash != "h" {
		t.Errorf("entry not loaded: %+v", got)
	}
	if old {
		t.Error("entry not used for 90 days was kept")
	}
}

func TestVerifiedHashOncePerFile(t *testing.T) {
	resetApkCache()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.apk")
	os.WriteFile(a, []byte("content"), 0o644)
	h1, err := verifiedHash(a)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := calculateHash(a); h1 != want {
		t.Errorf("hash %s, want %s", h1, want)
	}
	// The hash is remembered for the file: a hard link is not hashed again
	key, _ := fileKey(a)
	apkCache.hashes[key] = "remembered"
	b := filepath.Join(dir, "b.apk")
	if os.Link(a, b) != nil {
		t.Skip("no hard links")
	}
	if h, _ := verifiedHash(b); h != "remembered" {
		t.Errorf("hard link hashed again: %s", h)
	}
}

func TestFileKeyCoversMiddleOfSmallFiles(t *testing.T) {
	// A file between 64 and 128 KiB: every byte is in the key
	dir := t.TempDir()
	a := filepath.Join(dir, "a.apk")
	data := make([]byte, 100<<10)
	os.WriteFile(a, data, 0o644)
	st, _ := os.Stat(a)
	k1, _ := fileKey(a)
	data[70<<10] = 1
	os.WriteFile(a, data, 0o644)
	os.Chtimes(a, st.ModTime(), st.ModTime())
	if k2, _ := fileKey(a); k1 == k2 {
		t.Error("change at 70 KiB not in the key")
	}
}
