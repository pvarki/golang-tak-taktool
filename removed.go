package main

// Apks that leave the update folder are never deleted directly: they are
// moved into the removed folder of the apk folder (apks/removed/), with the
// version they came from as a prefix (5.6.0-vns_plugin.apk). -clean removes
// the ones that are certainly not needed there, -cleanall all of them.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const removedFolderName = "removed"

func removedDir(cfg *Config) string { return filepath.Join(cfg.Dir, removedFolderName) }

// shortRemovedDir is the removed folder for messages, e.g. "apks/removed/".
func shortRemovedDir(cfg *Config) string {
	return filepath.Base(cfg.Dir) + string(filepath.Separator) + removedFolderName + string(filepath.Separator)
}

// moveToRemoved moves the apk src into dir as prefix-<name>. If a file with
// that name exists and is the same file (a hard link), src is only removed;
// otherwise a number is added to the name. It works across file systems.
func moveToRemoved(src, dir, prefix string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(src)
	if prefix != "" {
		base = prefix + "-" + base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	srcSt, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		dst := filepath.Join(dir, name)
		st, err := os.Stat(dst)
		if err == nil {
			if os.SameFile(srcSt, st) {
				return dst, os.Remove(src)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := os.Rename(src, dst); err == nil {
			return dst, nil
		}
		// Another file system: copy, then remove
		if err := linkOrCopy(src, dst); err != nil {
			os.Remove(dst)
			return "", err
		}
		return dst, os.Remove(src)
	}
}

// retireFolder empties folder: apks for which keep returns false are moved
// to the removed folder (prefixed with version), also apks in subfolders; the
// rest is deleted, then the folder itself. keep gets the path relative to
// folder and may be nil (keep nothing).
func retireFolder(folder, version, removed string, keep func(name, path string) bool) error {
	if _, err := os.Stat(folder); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isApkFile(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(folder, path)
		if err != nil {
			return err
		}
		if keep != nil && keep(rel, path) {
			return nil
		}
		if _, err := moveToRemoved(path, removed, version); err != nil {
			return fmt.Errorf("moving %s to %s: %w", rel, removed, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(folder)
}

// keepNames keeps the apks with the given names (the old folder of a
// version that was just rebuilt from those files).
func keepNames(names map[string]bool) func(string, string) bool {
	return func(name, _ string) bool { return names[name] }
}

// keepIfIn keeps the apks that dir has with the same content (a hard link,
// or the same size and SHA-256). Names alone do not tell: a version folder
// names every revision of VNS vns_plugin.apk.
func keepIfIn(dir string) func(string, string) bool {
	return func(name, path string) bool {
		other := filepath.Join(dir, name)
		a, errA := os.Stat(path)
		b, errB := os.Stat(other)
		if errA != nil || errB != nil {
			return false
		}
		if os.SameFile(a, b) {
			return true
		}
		if a.Size() != b.Size() {
			return false
		}
		ha, errA := verifiedHash(path)
		hb, errB := verifiedHash(other)
		return errA == nil && errB == nil && ha == hb
	}
}

// removedEntry is an apk in the removed folder.
type removedEntry struct {
	file      string
	removable bool   // certainly not needed: -clean removes it
	reason    string // why it is removable
}

// planRemovedFolder lists the removed folder. With readApks, each apk is read
// to tell whether it is certainly not needed (-clean removes these): it is not
// an apk at all, the same apk is distributed, or a newer revision of its
// package is distributed for the ATAK version it is made for. Apks moved
// there by the run itself are not in the list, so they are only removed by
// the next run.
func (p *repoPlan) planRemovedFolder(readApks bool) error {
	dir := removedDir(p.cfg)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	p.removedFolder = nil
	for _, e := range entries {
		if e.IsDir() || !isApkFile(e.Name()) {
			continue
		}
		re := removedEntry{file: e.Name()}
		if readApks {
			path := filepath.Join(dir, e.Name())
			if lfs, _ := isLFSPointer(path); lfs {
				re.removable, re.reason = true, "Git LFS pointer, not an apk"
			} else if info, err := getApkData(path); err == nil {
				re.reason = p.supersededBy(info, path)
				re.removable = re.reason != ""
			}
		}
		p.removedFolder = append(p.removedFolder, re)
	}
	return nil
}

// supersededBy tells why the update folder makes the apk at path (in the
// removed folder) unnecessary, or returns "": the same apk is in a version
// folder, or a newer revision of its package, signed with the same
// certificate, is distributed for the ATAK version the apk is made for. That
// version is the one found in the apk, or else the version folder it was
// taken out of (the prefix of its file name); an app that is not made for an
// ATAK version may be replaced in any version. Builds for other ATAK versions
// do not count: a newer VNS for 5.8.0 does not replace VNS for 5.7.0. Nothing
// replaces it while the apk folder has two builds of the newest revision of
// the package (until that is resolved).
func (p *repoPlan) supersededBy(info ApkInfo, path string) string {
	for _, vp := range p.versions {
		if a, ok := vp.apks[info.Package]; ok && a.info.Hash == info.Hash {
			return fmt.Sprintf("the same apk is in %s", vp.version)
		}
	}
	prefix, name, _ := strings.Cut(filepath.Base(path), "-")
	var versions []string
	switch t := detectAtakTarget(info, name); {
	case t.Version != "":
		versions = []string{t.Version}
	case info.Type != "plugin" && !isAtakApp(info.Package):
		versions = p.cfg.Versions
	default:
		versions = []string{prefix}
	}
	r := (&repoApk{info: info}).revision()
	for _, vp := range p.versions {
		if !slices.Contains(versions, vp.version) || p.conflicts[vp.version][info.Package] {
			continue
		}
		if a, ok := vp.apks[info.Package]; ok && a.revision() > r && sameSigner(a.path, path) {
			// Full versionNames: they may tell a special build ("2.0-legacy")
			return fmt.Sprintf("%s %s (revision %s), replaced by %s %s (revision %s) in %s",
				info.DisplayName, info.Version, info.Revision, a.info.DisplayName, a.info.Version, a.info.Revision, vp.version)
		}
	}
	return ""
}

// cleanRemovedCount returns the number of apks of the removed folder that the
// clean mode removes.
func (p *repoPlan) cleanRemovedCount(m cleanMode) int {
	n := 0
	for _, r := range p.removedFolder {
		if m == cleanAll || (m == cleanSafe && r.removable) {
			n++
		}
	}
	return n
}
