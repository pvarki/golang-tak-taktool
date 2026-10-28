package main

// Reading an apk means hashing the whole file (SHA-256) and parsing its
// manifest. Apks can be over a gigabyte and the same apk is in several
// version folders, so the results are cached:
//
//   - in memory for the run, so each file is read once, and
//   - on disk between runs (taktool/apk-cache.json in the user's cache
//     folder, e.g. %LocalAppData% on Windows, ~/.cache on Linux).
//
// A file is recognised by its size, modification time and the SHA-256 of
// its first and last 64 KiB, so a moved, renamed or hard linked apk is still
// found, and a changed one is read again. (A file changed in the middle with
// its modification time kept, e.g. copied over with robocopy, is not noticed;
// 'taktool check' always hashes the whole file and finds it.) The cache is
// only used by the same taktool build that wrote it. Set TAKTOOL_CACHE=off to
// disable the cache on disk, or to a folder to keep it there.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// apkCacheSchema changes whenever what is read from an apk changes, so that
// old cache entries are not used.
const apkCacheSchema = 1

type cachedApk struct {
	Info     ApkInfo
	LastUsed string // date, entries not used for 90 days are dropped
}

type apkCacheFile struct {
	Schema  int
	Build   string // taktool version and commit: another build reads apks anew
	Entries map[string]cachedApk
}

// cacheBuild identifies the taktool build for the cache: a cache written by
// another build is not used, in case reading apks has changed. The version
// and commit are not enough: a build without Git information (e.g.
// -buildvcs=false in Docker) has none, so the size and modification time of
// the program are added.
func cacheBuild() string {
	b := versionString()
	if exe, err := os.Executable(); err == nil {
		if st, err := os.Stat(exe); err == nil {
			b += fmt.Sprintf(" %d %d", st.Size(), st.ModTime().Unix())
		}
	}
	return b
}

// apkCache is used by one goroutine only.
var apkCache = struct {
	loaded  bool
	dirty   bool
	entries map[string]cachedApk
	// in memory only, for this run
	variants map[string]variantResult
	hashes   map[string]string
	certs    map[string]certResult
	icons    map[string]iconResult
}{}

type variantResult struct {
	variant, warning string
	err              error
}

type certResult struct {
	fps []string
	err error
}

type iconResult struct {
	data  []byte
	kind  string
	notes []string
	err   error
}

func apkCacheFilePath() string {
	switch v := os.Getenv("TAKTOOL_CACHE"); v {
	case "off":
		return ""
	case "":
	default:
		return filepath.Join(v, "apk-cache.json")
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "taktool", "apk-cache.json")
}

// resetApkCache forgets everything in memory (for tests).
func resetApkCache() {
	apkCache.loaded, apkCache.dirty = false, false
}

func loadApkCache() {
	if apkCache.loaded {
		return
	}
	apkCache.loaded = true
	apkCache.entries = map[string]cachedApk{}
	apkCache.variants = map[string]variantResult{}
	apkCache.hashes = map[string]string{}
	apkCache.certs = map[string]certResult{}
	apkCache.icons = map[string]iconResult{}
	path := apkCacheFilePath()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var f apkCacheFile
	if json.Unmarshal(b, &f) != nil || f.Schema != apkCacheSchema || f.Build != cacheBuild() {
		return
	}
	for k, v := range f.Entries {
		apkCache.entries[k] = v
	}
}

// saveApkCache writes the cache to disk if anything was added. Errors are
// ignored: the cache is only an optimisation.
func saveApkCache() {
	path := apkCacheFilePath()
	if !apkCache.dirty || path == "" {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -90).Format(time.DateOnly)
	f := apkCacheFile{Schema: apkCacheSchema, Build: cacheBuild(), Entries: map[string]cachedApk{}}
	for k, v := range apkCache.entries {
		if v.LastUsed >= cutoff {
			f.Entries[k] = v
		}
	}
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if os.WriteFile(tmp, b, 0o644) != nil {
		os.Remove(tmp)
		return
	}
	if os.Rename(tmp, path) != nil {
		os.Remove(tmp)
		return
	}
	apkCache.dirty = false
}

// fileKey identifies the content of a file: its size, modification time and
// the SHA-256 of its first and last 64 KiB.
func fileKey(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	const part = 64 << 10
	h := sha256.New()
	if _, err := io.CopyN(h, f, part); err != nil && err != io.EOF {
		return "", err
	}
	if st.Size() > part {
		if _, err := f.Seek(max(part, st.Size()-part), io.SeekStart); err != nil {
			return "", err
		}
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%d:%d:%x", st.Size(), st.ModTime().UnixNano(), h.Sum(nil)[:12]), nil
}

// cachedApkData returns getApkData(path), from the cache if the file has been
// read before. fresh is true if the file was read (and hashed) now.
func cachedApkData(path string) (info ApkInfo, fresh bool, err error) {
	key, err := fileKey(path)
	if err != nil {
		return ApkInfo{}, false, err
	}
	loadApkCache()
	c, ok := apkCache.entries[key]
	today := time.Now().Format(time.DateOnly)
	if ok {
		if c.LastUsed != today {
			c.LastUsed = today
			apkCache.entries[key] = c
			apkCache.dirty = true
		}
		info = c.Info
		info.ApkPath = path
		return info, false, nil
	}
	info, err = readApkData(path)
	if err != nil {
		return ApkInfo{}, true, err
	}
	stored := info
	stored.ApkPath = ""
	apkCache.entries[key] = cachedApk{Info: stored, LastUsed: today}
	apkCache.hashes[key] = info.Hash
	apkCache.dirty = true
	return info, true, nil
}

// getApkData reads the data of an apk for product.inf (cached).
func getApkData(path string) (ApkInfo, error) {
	info, _, err := cachedApkData(path)
	return info, err
}

// verifiedHash returns the SHA-256 of a file, computed in this run (once per
// file, also if it is hard linked into several folders).
func verifiedHash(path string) (string, error) {
	key, err := fileKey(path)
	if err != nil {
		return "", err
	}
	loadApkCache()
	h, ok := apkCache.hashes[key]
	if ok {
		return h, nil
	}
	h, err = calculateHash(path)
	if err != nil {
		return "", err
	}
	apkCache.hashes[key] = h
	return h, nil
}

// memoByFile returns the result of fn for the file at path (and extra),
// computed once per run.
func memoByFile[T any](path, extra string, m func() map[string]T, fn func() T) T {
	key, err := fileKey(path)
	if err != nil {
		return fn()
	}
	key += "|" + extra
	loadApkCache()
	v, ok := m()[key]
	if ok {
		return v
	}
	v = fn()
	m()[key] = v
	return v
}

func cachedAtakVariant(path string, info ApkInfo) (string, string, error) {
	r := memoByFile(path, info.Version, func() map[string]variantResult { return apkCache.variants }, func() variantResult {
		v, w, err := detectAtakVariant(path, info)
		return variantResult{v, w, err}
	})
	return r.variant, r.warning, r.err
}

func cachedSigningCerts(path string) ([]string, error) {
	r := memoByFile(path, "", func() map[string]certResult { return apkCache.certs }, func() certResult {
		fps, err := signingCerts(path)
		return certResult{fps, err}
	})
	return r.fps, r.err
}

func cachedApkIcon(apkPath, iconPath string) ([]byte, string, []string, error) {
	r := memoByFile(apkPath, iconPath, func() map[string]iconResult { return apkCache.icons }, func() iconResult {
		d, k, notes, err := apkIcon(apkPath, iconPath)
		return iconResult{d, k, notes, err}
	})
	return r.data, r.kind, r.notes, r.err
}
