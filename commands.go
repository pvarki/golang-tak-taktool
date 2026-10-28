package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

//go:embed taktool.example.yaml
var exampleConfig []byte

// InitFolder creates the folders for 'taktool repo' in base:
//
//	base/apks/taktool.yaml   (from taktool.example.yaml)
//	base/apks/images/
//	base/update/
//	base/manuals/
//
// Nothing that exists already is changed.
func InitFolder(w io.Writer, base string) error {
	// Run in the apk folder by mistake: it would get apks/, update/ and
	// manuals/ inside it
	if _, err := os.Stat(filepath.Join(base, configFileName)); err == nil {
		abs, _ := filepath.Abs(base)
		return fmt.Errorf("%s has %s, so it is an apk folder already; run 'taktool init' in the folder above it "+
			"('taktool init ..'), or 'taktool repo' here", abs, configFileName)
	}
	for _, d := range []string{"apks", filepath.Join("apks", "images"), "update", "manuals"} {
		p := filepath.Join(base, d)
		if _, err := os.Stat(p); err == nil {
			fmt.Fprintf(w, "exists:  %s\n", p)
			continue
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return err
		}
		fmt.Fprintf(w, "created: %s\n", p)
	}

	cfg := filepath.Join(base, "apks", configFileName)
	if _, err := os.Stat(cfg); err == nil {
		fmt.Fprintf(w, "exists:  %s (not changed)\n", cfg)
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(cfg, exampleConfig, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "created: %s\n", cfg)
	} else {
		return err
	}

	fmt.Fprintf(w, "\nNext: put the apks into %s, edit %s, and run there:\n",
		filepath.Join(base, "apks"), configFileName)
	fmt.Fprintln(w, "  taktool repo -dry-run")
	return nil
}

type manualsOptions struct {
	config  string
	dryRun  bool
	verbose bool
}

// CopyManuals copies the manuals of the apks in the update folder into
// the manuals folder of taktool.yaml.
func CopyManuals(o manualsOptions, out io.Writer) error {
	cfg, err := loadConfig(o.config)
	if err != nil {
		return err
	}
	if cfg.Manuals.Folder == "" {
		return fmt.Errorf("no manuals folder; set manuals: folder: in %s", configFileName)
	}

	p := &repoPlan{cfg: cfg}
	for _, v := range cfg.Versions {
		vp, err := p.loadVersionFolder(filepath.Join(cfg.Output, v), v)
		if err != nil {
			return err
		}
		if vp == nil {
			p.warn("%s: no version folder in %s; run 'taktool repo' first", v, cfg.Output)
			continue
		}
		p.versions = append(p.versions, vp)
	}
	if err := p.planManuals(); err != nil {
		return err
	}

	if o.dryRun {
		fmt.Fprintln(out, "Dry run, nothing is changed.")
	}
	printManuals(out, p, o.verbose, true)
	if len(p.warnings) > 0 {
		fmt.Fprintln(out, "\nWarnings:")
		for _, s := range p.warnings {
			fmt.Fprintf(out, "  %s\n", s)
		}
	}
	if o.dryRun || len(p.manuals) == 0 {
		return nil
	}
	return writeManuals(cfg.Manuals.Folder, p.manuals)
}

// loadVersionFolder reads the apks of a version folder for the manuals. It
// returns nil if the folder does not exist. Apks that cannot be read are
// skipped with a warning.
func (p *repoPlan) loadVersionFolder(dir, version string) (*versionPlan, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	vp := &versionPlan{version: version, exists: true, apks: map[string]*repoApk{}}
	for _, e := range entries {
		if e.IsDir() || !isApkFile(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if lfs, _ := isLFSPointer(path); lfs {
			p.warn("%s/%s is a Git LFS pointer, not an apk", version, e.Name())
			continue
		}
		info, err := getApkData(path)
		if err != nil {
			p.warn("%s/%s cannot be read (%v); its manuals were not copied", version, e.Name(), err)
			continue
		}
		vp.apks[info.Package] = &repoApk{info: info, path: path}
	}
	return vp, nil
}

// printManuals prints the manuals to add or update and, with verbose, the
// other PDFs that are not copied. always prints "up to date" when there is
// nothing to do (for 'taktool manuals').
func printManuals(w io.Writer, p *repoPlan, verbose, always bool) {
	folder := p.cfg.Manuals.Folder
	if folder == "" {
		return
	}
	notes := verbose && len(p.manualNotes) > 0
	if len(p.manuals) == 0 && !notes {
		if always {
			fmt.Fprintf(w, "\nManuals (%s): up to date\n", folder)
		}
		return
	}
	fmt.Fprintf(w, "\nManuals (%s):\n", folder)
	for _, m := range p.manuals {
		fmt.Fprintf(w, "  %s %s\n", m.state, m.name)
	}
	if notes {
		for _, n := range p.manualNotes {
			fmt.Fprintf(w, "  %s\n", n)
		}
	}
	if len(p.manuals) == 0 {
		fmt.Fprintln(w, "  up to date")
	}
}
