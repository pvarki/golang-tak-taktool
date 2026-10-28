package main

// Applying the plan of 'taktool repo' to the update folder.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// executeRepoPlan applies the plan. Problems that leave the update folder
// complete (e.g. a leftover folder that cannot be deleted) are returned as
// notes instead of errors.
func executeRepoPlan(p *repoPlan, clean cleanMode, log io.Writer) (notes []string, err error) {
	cfg := p.cfg
	removed := removedDir(cfg)
	if err := os.MkdirAll(cfg.Output, 0o755); err != nil {
		return nil, err
	}

	// Before the version folders are rebuilt: the apks are read from where
	// they are now
	if err := writeManuals(cfg.Manuals.Folder, p.manuals); err != nil {
		return nil, err
	}

	for _, vp := range p.versions {
		if !vp.hasChanges() {
			continue // up to date, also its product.infz
		}
		n, err := buildVersionFolder(cfg, vp, log)
		notes = append(notes, n...)
		if err != nil {
			return notes, fmt.Errorf("%s: %w", vp.version, err)
		}
	}

	if err := writeRepositoriesInf(cfg.Output, cfg.Versions); err != nil {
		return notes, err
	}

	// Folders that are not version folders in versions (e.g. 5.5.0) and
	// loose apks: the apks go to the removed folder. Other files are deleted.
	for _, n := range p.remove {
		path := filepath.Join(cfg.Output, n)
		st, err := os.Stat(path)
		switch {
		case err != nil:
			continue
		case st.IsDir():
			err = retireFolder(path, n, removed, nil)
		case isApkFile(n):
			_, err = moveToRemoved(path, removed, "update")
		default:
			err = os.RemoveAll(path)
		}
		if err != nil {
			return notes, err
		}
	}

	notes = append(notes, cleanupLeftoverFolders(cfg, removed)...)

	// The apks are in the version folders now
	for _, f := range p.moved {
		if err := os.Remove(f); err != nil {
			return notes, err
		}
	}

	for _, l := range p.leftovers {
		if l.removedBy(clean) {
			if err := os.Remove(filepath.Join(cfg.Dir, l.file)); err != nil {
				return notes, err
			}
			p.cleaned++
		}
	}
	if clean != cleanNone {
		// The apks that were in the removed folder before this run (as in the
		// report and the question); the ones this run moved there stay until
		// the next run, so that the report always shows them first
		for _, r := range p.removedFolder {
			if clean == cleanAll || r.removable {
				if err := os.Remove(filepath.Join(removed, r.file)); err != nil {
					return notes, err
				}
				p.cleaned++
			}
		}
		os.Remove(removed) // only if empty
	}
	return notes, nil
}

// cleanupLeftoverFolders removes what interrupted runs left in the update
// folder: .<version>.tmp folders, .<version>.old folders next to a complete
// version folder, and .<version>.old folders of versions that are no longer
// in versions. The apks of a .old folder that the version folder does not
// have (with the same content) go to the removed folder. A .old folder of a
// listed version without its version folder is kept: the next run restores
// it.
func cleanupLeftoverFolders(cfg *Config, removed string) (notes []string) {
	entries, err := os.ReadDir(cfg.Output)
	if err != nil {
		return []string{fmt.Sprintf("cannot list %s: %v", cfg.Output, err)}
	}
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || !strings.HasPrefix(n, ".") {
			continue
		}
		path := filepath.Join(cfg.Output, n)
		switch {
		case strings.HasSuffix(n, ".tmp") && versionRe.MatchString(strings.TrimSuffix(n[1:], ".tmp")):
			if err := os.RemoveAll(path); err != nil {
				notes = append(notes, fmt.Sprintf("cannot remove %s: %v; remove it by hand", path, err))
			}
		case strings.HasSuffix(n, ".old") && versionRe.MatchString(strings.TrimSuffix(n[1:], ".old")):
			v := strings.TrimSuffix(n[1:], ".old")
			keep := keepIfIn(filepath.Join(cfg.Output, v))
			if _, err := os.Stat(filepath.Join(cfg.Output, v)); err != nil {
				if slices.Contains(cfg.Versions, v) {
					notes = append(notes, fmt.Sprintf("%s is kept: %s is missing; the next 'taktool repo' restores it", path, v))
					continue
				}
				keep = nil // the version is not distributed any more
			}
			if err := retireFolder(path, v, removed, keep); err != nil {
				notes = append(notes, fmt.Sprintf("cannot remove %s: %v; remove it by hand", path, err))
			}
		}
	}
	return notes
}

// buildVersionFolder builds the version folder in a temporary folder and
// swaps it in place when it is complete, so TAK Server never serves a half
// built folder. The apks that the old folder had but the new one does not go
// to the removed folder.
func buildVersionFolder(cfg *Config, vp *versionPlan, log io.Writer) (notes []string, err error) {
	dir := filepath.Join(cfg.Output, vp.version)
	tmp := filepath.Join(cfg.Output, "."+vp.version+".tmp")
	old := filepath.Join(cfg.Output, "."+vp.version+".old")

	// A .tmp folder is always a leftover; a .old folder only if the version
	// folder is complete (otherwise it is restored from)
	if err := os.RemoveAll(tmp); err != nil {
		return nil, err
	}
	if !vp.restoring {
		if _, err := os.Stat(old); err == nil {
			if err := retireFolder(old, vp.version, removedDir(cfg), keepIfIn(dir)); err != nil {
				return nil, err
			}
		}
	}
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return nil, err
	}

	// The apks of the existing folder that stay (by name in the old folder)
	keep := map[string]bool{}
	infos := vp.productInfos()
	for i := range infos {
		if d := filepath.Dir(infos[i].srcPath); d == dir || d == old {
			keep[filepath.Base(infos[i].srcPath)] = true
		}
		if err := linkOrCopy(infos[i].srcPath, filepath.Join(tmp, infos[i].ApkPath)); err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
		infos[i].srcPath = ""
	}
	if err := writeProductInfz(tmp, infos, filepath.Join(cfg.Dir, "images"), log); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}

	if vp.exists {
		if err := os.Rename(dir, old); err != nil {
			os.RemoveAll(tmp)
			return nil, fmt.Errorf("cannot replace %s (is a file in it open?): %w", dir, err)
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		if vp.exists {
			// Put the old folder back
			if rerr := os.Rename(old, dir); rerr != nil {
				return nil, fmt.Errorf("%w; also restoring %s failed (%v): the next run restores it", err, dir, rerr)
			}
		}
		os.RemoveAll(tmp)
		return nil, err
	}
	if err := retireFolder(old, vp.version, removedDir(cfg), keepNames(keep)); err != nil {
		notes = append(notes, fmt.Sprintf("%s: the folder is ready, but %s could not be removed (%v); the next run removes it", vp.version, old, err))
	}
	return notes, nil
}

// linkOrCopy hard links src to dst, or copies it if a link is not possible
// (e.g. different file systems).
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// Copy to a temporary name and rename when complete, so a crash never
	// leaves a partial apk under a real name
	part := dst + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(part)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(part)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, dst)
}

// writeRepositoriesInf writes the ATAK versions, one per line in ascending
// order, e.g.
//
//	5.6.0
//	5.7.0
//	5.8.0
func writeRepositoriesInf(dir string, versions []string) error {
	name := filepath.Join(dir, repositoriesInfFilename)
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(versions, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}
