package main

// Planning of 'taktool repo': which apks go to which version folder.

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// setVariant detects the ATAK variant of an ATAK apk.
func (p *repoPlan) setVariant(a *repoApk) {
	if !isAtakApp(a.info.Package) {
		return
	}
	v, warning, err := cachedAtakVariant(a.path, a.info)
	if err != nil {
		p.warn("%s: cannot tell ATAK small from full: %v", filepath.Base(a.path), err)
		return
	}
	if warning != "" {
		p.warn("%s: %s", filepath.Base(a.path), warning)
	}
	a.variant = v
}

// atakPreferred reports whether a is a better choice than b for the same
// package: the ATAK variant set in taktool.yaml first, then the revision.
func (p *repoPlan) atakPreferred(a, b *repoApk) (better, sameRank bool) {
	want := p.cfg.Atak
	if a.variant != "" && b.variant != "" && a.variant != b.variant {
		return a.variant == want, false
	}
	return a.revision() > b.revision(), a.revision() == b.revision()
}

// checkOutputFolder refuses to work on a folder that does not look like an
// update folder, because everything else in it is deleted.
func checkOutputFolder(cfg *Config) error {
	src, out := cfg.Dir, cfg.Output
	if src == out {
		return fmt.Errorf("output folder must not be the apk folder (%s)", out)
	}
	if rel, err := filepath.Rel(out, src); err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("the apk folder %s must not be inside the output folder %s", src, out)
	}

	entries, err := os.ReadDir(out)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	for _, e := range entries {
		n := e.Name()
		if n == repositoriesInfFilename || n == "repository.inf" {
			return nil
		}
		if e.IsDir() && versionRe.MatchString(n) {
			if _, err := os.Stat(filepath.Join(out, n, productInfzFilename)); err == nil {
				return nil
			}
		}
	}
	return fmt.Errorf("%s does not look like an update folder (no repositories.inf, and no version folder with %s); "+
		"refusing to change its contents. Check 'output' in %s", out, productInfzFilename, configFileName)
}

func planRepository(cfg *Config) (*repoPlan, error) {
	if err := checkOutputFolder(cfg); err != nil {
		return nil, err
	}
	plan := &repoPlan{cfg: cfg}
	if cfg.outputDefault {
		if _, err := os.Stat(filepath.Join(cfg.Dir, "update", repositoriesInfFilename)); err == nil {
			plan.warn("%s has no output, so the update folder is %s; %s is an update folder too (the default was "+
				"update before): set output in %s if that is the one TAK Server serves", configFileName, cfg.Output,
				filepath.Join(cfg.Dir, "update"), configFileName)
		}
	}

	// Apks of the apk folder for each version
	candidates, results, err := plan.scanSource()
	if err != nil {
		return nil, err
	}

	for _, v := range cfg.Versions {
		vp, err := plan.planVersion(v, candidates[v], results)
		if err != nil {
			return nil, err
		}
		plan.versions = append(plan.versions, vp)
	}

	plan.warnExcludedDistributed()

	// Apks of the apk folder that were not used in any version
	srcFiles := make([]string, 0, len(results))
	for f := range results {
		srcFiles = append(srcFiles, f)
	}
	slices.Sort(srcFiles)
	for _, f := range srcFiles {
		r := results[f]
		if r.used {
			plan.moved = append(plan.moved, f)
		} else {
			if r.reason == "" {
				r.reason = "not used"
			}
			plan.leftovers = append(plan.leftovers, leftover{file: filepath.Base(f), reason: r.reason,
				removable: r.removable, expected: r.expected, otherAtak: r.otherAtak})
		}
	}

	if err := plan.planOutputCleanup(); err != nil {
		return nil, err
	}
	if cfg.Manuals.WithRepo {
		if err := plan.planManuals(); err != nil {
			return nil, err
		}
	}
	if err := plan.planIcons(); err != nil {
		return nil, err
	}
	return plan, nil
}

// scanSource reads the apks of the apk folder and decides which versions
// each one goes to.
func (p *repoPlan) scanSource() (map[string][]*repoApk, map[string]*sourceResult, error) {
	cfg := p.cfg
	entries, err := os.ReadDir(cfg.Dir)
	if err != nil {
		return nil, nil, err
	}

	candidates := map[string][]*repoApk{}
	results := map[string]*sourceResult{}

	for _, e := range entries {
		if e.IsDir() || !isApkFile(e.Name()) {
			continue
		}
		path := filepath.Join(cfg.Dir, e.Name())
		res := &sourceResult{}
		results[path] = res

		if pattern, ok := cfg.excluded(e.Name()); ok {
			res.reason, res.expected = fmt.Sprintf("excluded in %s (%s)", configFileName, pattern), true
			if lfs, _ := isLFSPointer(path); !lfs {
				if info, err := getApkData(path); err == nil {
					p.excludedFiles = append(p.excludedFiles, excludedFile{e.Name(), pattern, info})
				}
			}
			continue
		}

		if lfs, err := isLFSPointer(path); err != nil {
			return nil, nil, err
		} else if lfs {
			res.reason, res.removable = "Git LFS pointer, not an apk", true
			continue
		}

		info, err := getApkData(path)
		if err != nil {
			res.reason = "cannot read: " + err.Error()
			continue
		}
		a := &repoApk{info: info, path: path}
		p.setVariant(a)

		if pattern, ok := cfg.excluded(info.Package); ok {
			res.reason, res.expected = fmt.Sprintf("%s (%s) excluded in %s (%s)", a.name(), info.Package, configFileName, pattern), true
			continue
		}
		if !cfg.wantsPackage(info.Package) {
			res.reason, res.expected = fmt.Sprintf("%s (%s) is not in packages; add it to %s to distribute it", a.name(), info.Package, configFileName), true
			continue
		}
		if isTemplatePackage(info.Package) {
			res.reason, res.expected = "uses the package name of ATAK's plugin template", true
			p.templates = append(p.templates, templateApk{a.name(), info.Package, ""})
			continue
		}

		a.target = detectAtakTarget(info, e.Name())
		if a.target.Flavor == "MIL" || a.target.Flavor == "GOV" {
			res.reason = fmt.Sprintf("%s product; only CIV is supported", a.target.Flavor)
			p.warn("%s: %s is a %s product; only CIV is supported, the apk was not distributed", e.Name(), a.name(), a.target.Flavor)
			continue
		}

		var versions []string
		if vs, ok := cfg.targetVersions(info.Package); ok {
			// Set in taktool.yaml
			versions = vs
		} else {
			switch {
			case a.target.Version != "":
				if !slices.Contains(cfg.Versions, a.target.Version) {
					res.reason = fmt.Sprintf("made for ATAK %s (from %s), which is not in versions", a.target.Version, a.target.Source)
					res.otherAtak = a.target.Version
					continue
				}
				versions = []string{a.target.Version}
			case info.Type == "plugin" || isAtakApp(info.Package):
				res.reason = fmt.Sprintf("ATAK version not found in the apk or its file name; set it under targets in %s (%s: [5.8.0])",
					configFileName, info.Package)
				continue
			default:
				// An app that does not depend on an ATAK version (e.g. OpenTAK ICU)
				versions = cfg.Versions
			}
		}

		for _, v := range versions {
			candidates[v] = append(candidates[v], a)
		}
	}

	// ATAK small and full: if the apk folder has the variant set in
	// taktool.yaml for a version, leave the other one; otherwise distribute
	// the other one with a warning
	for v, list := range candidates {
		candidates[v] = p.pickAtakVariant(v, list, results)
	}

	// If the apk folder has several apks of the same package for a version,
	// keep the newest
	for v, list := range candidates {
		candidates[v] = p.newestPerPackage(v, list, results)
	}
	return candidates, results, nil
}

// pickAtakVariant leaves out the ATAK apks of the other variant than the one
// set in taktool.yaml, if the apk folder has the wanted variant for v.
func (p *repoPlan) pickAtakVariant(v string, list []*repoApk, results map[string]*sourceResult) []*repoApk {
	want := p.cfg.Atak
	hasWanted := map[string]bool{} // package -> wanted variant found
	for _, a := range list {
		if a.variant == want {
			hasWanted[a.info.Package] = true
		}
	}
	var out []*repoApk
	for _, a := range list {
		if a.variant != "" && a.variant != want && hasWanted[a.info.Package] {
			setExpected(results, a.path, fmt.Sprintf("ATAK %s, but atak: %s in %s", a.variant, want, configFileName))
			continue
		}
		out = append(out, a)
	}
	return out
}

// newestPerPackage keeps the highest revision of each package among apks of
// the apk folder for version v. If the highest revision has several apks with
// different content, none of them is distributed, and the older revisions are
// kept until that is resolved. Older revisions are never removed by -clean
// (only -cleanall), as the newest may be a special build.
func (p *repoPlan) newestPerPackage(v string, list []*repoApk, results map[string]*sourceResult) []*repoApk {
	byPkg := map[string][]*repoApk{}
	var order []string
	for _, a := range list {
		if _, seen := byPkg[a.info.Package]; !seen {
			order = append(order, a.info.Package)
		}
		byPkg[a.info.Package] = append(byPkg[a.info.Package], a)
	}
	var out []*repoApk
	for _, pkg := range order {
		apks := byPkg[pkg]
		best := apks[0]
		for _, a := range apks[1:] {
			if a.revision() > best.revision() {
				best = a
			}
		}
		// The apks of the highest revision, one per content
		var top []*repoApk
		for _, a := range apks {
			if a.revision() != best.revision() {
				continue
			}
			dup := false
			for _, t := range top {
				if t.info.Hash == a.info.Hash {
					setResult(results, a.path, "copy of "+filepath.Base(t.path), true)
					dup = true
					break
				}
			}
			if !dup {
				top = append(top, a)
			}
		}
		if len(top) > 1 {
			var names []string
			for _, t := range top {
				names = append(names, filepath.Base(t.path))
			}
			p.warn("%s have the same revision %s but different content; none of them was distributed",
				strings.Join(names, ", "), best.info.Revision)
			for _, t := range top {
				setResult(results, t.path, "same revision as another apk of the same package but different content", false)
			}
			if p.conflicts == nil {
				p.conflicts = map[string]map[string]bool{}
			}
			if p.conflicts[v] == nil {
				p.conflicts[v] = map[string]bool{}
			}
			p.conflicts[v][pkg] = true
		}
		for _, a := range apks {
			if a.revision() >= best.revision() {
				continue
			}
			if len(top) > 1 {
				setResult(results, a.path, fmt.Sprintf("older (revision %s) than %s, kept until the conflict of revision %s is resolved",
					a.info.Revision, filepath.Base(best.path), best.info.Revision), false)
			} else {
				// Left on purpose, but not removed by -clean: the newer one may
				// be a special build (WAVE Legacy) that should be excluded
				setExpected(results, a.path, fmt.Sprintf("older (revision %s) than %s (revision %s) in the apk folder",
					a.info.Revision, filepath.Base(best.path), best.info.Revision))
			}
		}
		if len(top) == 1 {
			out = append(out, top[0])
		}
	}
	return out
}

// warnExcludedDistributed warns about apks excluded by file name that are
// distributed anyway: the apk in the update folder has another name (it was
// renamed), so the pattern does not match it.
func (p *repoPlan) warnExcludedDistributed() {
	for _, x := range p.excludedFiles {
		var in []string
		for _, vp := range p.versions {
			if a, ok := vp.apks[x.info.Package]; ok && a.info.Hash == x.info.Hash {
				in = append(in, fmt.Sprintf("%s/%s", vp.version, filepath.Base(a.path)))
			}
		}
		if len(in) > 0 {
			p.warn("%s is excluded by its file name (%s), but the same apk is distributed as %s: exclude patterns only match "+
				"file names in the apk folder. To take it out, delete it from the version folder and run 'taktool repo' again",
				x.file, x.pattern, strings.Join(in, ", "))
		}
	}
}

// setResult sets the reason an apk of the apk folder was not used, unless
// the apk was used for another version.
func setResult(results map[string]*sourceResult, path, reason string, removable bool) {
	r := results[path]
	if r.used || r.reason != "" {
		return
	}
	r.reason, r.removable = reason, removable
}

// setExpected is setResult for an apk that is left on purpose.
func setExpected(results map[string]*sourceResult, path, reason string) {
	r := results[path]
	if r.used || r.reason != "" {
		return
	}
	r.reason, r.expected = reason, true
}

// planVersion merges the apks of the apk folder into a version folder.
func (p *repoPlan) planVersion(v string, candidates []*repoApk, results map[string]*sourceResult) (*versionPlan, error) {
	vp := &versionPlan{version: v, apks: map[string]*repoApk{}}
	if err := p.readVersionFolder(vp); err != nil {
		return nil, err
	}
	p.mergeCandidates(vp, candidates, results)
	if err := checkFileNames(vp); err != nil {
		return nil, err
	}
	if err := p.planRebuild(vp); err != nil {
		return nil, err
	}
	p.warnVersion(vp)
	return vp, nil
}

// readVersionFolder reads the apks that are in the version folder already,
// and decides which of them stay.
func (p *repoPlan) readVersionFolder(vp *versionPlan) error {
	cfg, v := p.cfg, vp.version
	dir := filepath.Join(cfg.Output, v)
	// Apks already in the version folder. If it is missing but an
	// interrupted run left .<version>.old, rebuild it from there.
	readDir := dir
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		old := filepath.Join(cfg.Output, "."+v+".old")
		if oe, oerr := os.ReadDir(old); oerr == nil {
			vp.restoring, readDir, entries = true, old, oe
			p.warn("%s: the version folder is missing but an earlier run left it as %s; it is restored from there", v, filepath.Base(old))
		}
	case err != nil:
		return err
	default:
		vp.exists = true
	}
	for _, e := range entries {
		path := filepath.Join(readDir, e.Name())
		if e.IsDir() || !isApkFile(e.Name()) {
			if e.Name() != productInfzFilename {
				vp.removed = append(vp.removed, e.Name())
			}
			continue
		}
		if lfs, err := isLFSPointer(path); err != nil {
			return err
		} else if lfs {
			p.warn("%s/%s is a Git LFS pointer, not an apk; removed", v, e.Name())
			vp.removed = append(vp.removed, e.Name()+" (Git LFS pointer)")
			continue
		}
		info, err := getApkData(path)
		if err != nil {
			// Maybe only a temporary problem (network drive, cloud file not
			// downloaded): never remove the apk because of it
			return fmt.Errorf("%s cannot be read (%v); nothing was changed. Fix it, or move the file away by hand, and run again", path, err)
		}
		a := &repoApk{info: info, path: path}
		p.setVariant(a)
		if vs, ok := cfg.targetVersions(info.Package); ok && !slices.Contains(vs, v) {
			vp.removed = append(vp.removed, a.name()+" (not in targets)")
			continue
		} else if !ok {
			if t := detectAtakTarget(info, e.Name()); t.Version != "" && t.Version != v {
				vp.removed = append(vp.removed, fmt.Sprintf("%s (made for ATAK %s)", a.name(), t.Version))
				continue
			}
		}
		if !cfg.wantsPackage(info.Package) {
			vp.removed = append(vp.removed, a.name()+" (not in packages)")
			continue
		}
		if isTemplatePackage(info.Package) {
			vp.removed = append(vp.removed, a.name()+" (template package name)")
			p.templates = append(p.templates, templateApk{a.name(), info.Package, v})
			continue
		}
		if pattern, ok := cfg.excluded(info.Package); ok {
			vp.removed = append(vp.removed, fmt.Sprintf("%s (excluded: %s)", a.name(), pattern))
			continue
		}
		if b, dup := vp.apks[info.Package]; dup {
			keep, drop := b, a
			if better, _ := p.atakPreferred(a, b); better {
				keep, drop = a, b
			}
			vp.apks[info.Package] = keep
			vp.removed = append(vp.removed, fmt.Sprintf("%s (older copy of %s)", filepath.Base(drop.path), keep.info.DisplayName))
			continue
		}
		vp.apks[info.Package] = a
	}

	return nil
}

// mergeCandidates merges the apks of the apk folder for the version into
// it: the newest revision wins (for ATAK, the variant set in taktool.yaml
// first).
func (p *repoPlan) mergeCandidates(vp *versionPlan, candidates []*repoApk, results map[string]*sourceResult) {
	v := vp.version
	for _, c := range candidates {
		res := results[c.path]
		e, exists := vp.apks[c.info.Package]
		var better, sameRank bool
		if exists {
			better, sameRank = p.atakPreferred(c, e)
		}
		switch {
		case !exists:
			vp.apks[c.info.Package] = c
			vp.added = append(vp.added, c.name())
			res.used = true
		case better && c.variant != e.variant:
			vp.apks[c.info.Package] = c
			vp.updated = append(vp.updated, fmt.Sprintf("%s -> %s", e.name(), c.name()))
			res.used = true
		case !better && !sameRank && c.variant != e.variant:
			setExpected(results, c.path, fmt.Sprintf("ATAK %s, but atak: %s in %s and %s is in %s",
				c.variant, p.cfg.Atak, configFileName, e.name(), v))
		case better:
			if signersDiffer(e.path, c.path) {
				p.warn("%s %s: the new apk is signed with a different certificate than the one it replaces; "+
					"devices cannot update it, the old version must be uninstalled first", v, c.info.DisplayName)
			}
			vp.apks[c.info.Package] = c
			vp.updated = append(vp.updated, fmt.Sprintf("%s %s -> %s", c.info.DisplayName, shortVersion(e.info.Version), shortVersion(c.info.Version)))
			res.used = true
		case !sameRank:
			// Not removed by -clean: the apk in the version folder may be
			// another build of the package (WAVE Legacy renamed to
			// wave_plugin.apk), and this one the build that should be there
			setResult(results, c.path, fmt.Sprintf("older (revision %s) than %s (revision %s) in %s",
				c.info.Revision, e.name(), e.info.Revision, v), false)
		case c.info.Hash == e.info.Hash:
			setResult(results, c.path, fmt.Sprintf("already in %s", v), true)
		default:
			p.warn("%s: %s has the same revision %s as the apk in %s but different content; kept the one in %s",
				filepath.Base(c.path), c.info.DisplayName, c.info.Revision, v, v)
			setResult(results, c.path, fmt.Sprintf("same revision as in %s but different content", v), false)
		}
	}

}

// checkFileNames checks that no two apks of the version get the same file
// name (case-insensitively).
func checkFileNames(vp *versionPlan) error {
	v := vp.version
	names := map[string]*repoApk{}
	for _, a := range vp.apks {
		n := strings.ToLower(preferredApkName(a.info))
		if b, dup := names[n]; dup {
			return fmt.Errorf("%s: %s (%s) and %s (%s) would both be named %s",
				v, a.info.DisplayName, a.info.Package, b.info.DisplayName, b.info.Package, preferredApkName(a.info))
		}
		names[n] = a
	}

	return nil
}

// planRebuild decides whether an existing version folder must be rebuilt
// although no apk changed: apks with other names, or a product.infz written
// by an older version of taktool or with other icons.
func (p *repoPlan) planRebuild(vp *versionPlan) error {
	dir := filepath.Join(p.cfg.Output, vp.version)
	if vp.exists && !vp.hasChanges() {
		for _, a := range vp.sortedApks() {
			if want := preferredApkName(a.info); filepath.Base(a.path) != want {
				vp.other = append(vp.other, fmt.Sprintf("rename %s -> %s", filepath.Base(a.path), want))
			}
		}
		why, err := p.productInfzChanges(vp, dir)
		if err != nil {
			return err
		}
		if why != "" {
			vp.other = append(vp.other, fmt.Sprintf("rebuild %s (%s)", productInfzFilename, why))
		}
	}

	return nil
}

// warnVersion warns about a version without apks or without ATAK, the
// other ATAK variant, and packages of taktool.yaml without an apk.
func (p *repoPlan) warnVersion(vp *versionPlan) {
	cfg, v := p.cfg, vp.version
	if len(vp.apks) == 0 {
		p.warn("%s: no apks to distribute", v)
	} else {
		hasAtak := false
		for pkg := range vp.apks {
			hasAtak = hasAtak || isAtakApp(pkg)
		}
		if !hasAtak {
			p.warn("%s: no ATAK apk", v)
		}
		for _, a := range vp.apks {
			if a.variant != "" && a.variant != cfg.Atak {
				p.warn("%s: no ATAK %s found, distributing ATAK %s", v, cfg.Atak, a.variant)
			}
		}
	}
	for _, pkg := range cfg.Packages {
		if _, ok := vp.apks[pkg]; !ok && !isAtakApp(pkg) {
			p.warn("%s: %s is in packages but there is no apk for it", v, pkg)
		}
	}
}

// productInfzChanges compares dir/product.infz with what building the
// version folder would write. It returns "" if they are the same, otherwise
// what differs.
func (p *repoPlan) productInfzChanges(vp *versionPlan, dir string) (string, error) {
	existing, err := os.ReadFile(filepath.Join(dir, productInfzFilename))
	if err != nil {
		return "missing", nil
	}
	infos := vp.productInfos()
	var want bytes.Buffer
	if err := buildProductInfz(&want, dir, infos, filepath.Join(p.cfg.Dir, "images"), nil); err != nil {
		return "", err
	}
	if bytes.Equal(existing, want.Bytes()) {
		return "", nil
	}
	old, err1 := zipContents(existing)
	cur, err2 := zipContents(want.Bytes())
	if err1 != nil || err2 != nil {
		return "unreadable", nil
	}
	var reasons, icons []string
	if !bytes.Equal(old[productInfFilename], cur[productInfFilename]) {
		reasons = append(reasons, "product.inf changed")
	}
	for _, a := range infos {
		if !bytes.Equal(old[a.IconPath], cur[a.IconPath]) {
			icons = append(icons, a.DisplayName)
		}
	}
	if len(icons) > 0 {
		reasons = append(reasons, "icons changed: "+strings.Join(icons, ", "))
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "zip layout changed")
	}
	return strings.Join(reasons, "; "), nil
}

// zipContents returns the files of a zip by name.
func zipContents(data []byte) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		files[f.Name] = b
	}
	return files, nil
}

// productInfos returns the product.inf data of the version folder: the apks
// with their file names in the folder, and where they are now.
func (vp *versionPlan) productInfos() []ApkInfo {
	var infos []ApkInfo
	for _, a := range vp.sortedApks() {
		info := a.info
		info.ApkPath = preferredApkName(a.info)
		info.srcPath = a.path
		infos = append(infos, info)
	}
	return infos
}

// planIcons checks the icon of each distributed app: apps without a usable
// icon and without a custom image, icons converted to png, and custom images
// that are the same as the apk's own icon or differ from it.
func (p *repoPlan) planIcons() error {
	imagesDir := filepath.Join(p.cfg.Dir, "images")
	custom, err := listCustomImages(imagesDir)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	used := map[string]bool{}
	for _, vp := range p.versions {
		for _, a := range vp.sortedApks() {
			if seen[a.info.Package] {
				continue
			}
			seen[a.info.Package] = true
			name := preferredApkName(a.info)
			c := customImageFor(a.info, strings.TrimSuffix(name, filepath.Ext(name))+".png", custom)
			used[c] = true
			own, kind, notes, iconErr := cachedApkIcon(a.path, a.info.IconPath)
			label := a.info.DisplayName
			switch {
			case c != "" && iconErr != nil:
				// the custom image is needed
			case c != "":
				mine, err := os.ReadFile(filepath.Join(imagesDir, c))
				if err != nil {
					return err
				}
				if bytes.Equal(mine, own) {
					p.sameCustom = append(p.sameCustom, fmt.Sprintf("%s: images/%s", label, c))
				} else {
					p.differentCustom = append(p.differentCustom, fmt.Sprintf("%s: images/%s (the apk has a %s icon)", label, c, kind))
				}
			case iconErr != nil:
				why := ""
				if len(notes) > 0 {
					why = "; not drawn: " + strings.Join(notes, ", ")
				}
				p.missingIcons = append(p.missingIcons, fmt.Sprintf("%s: images/%s (or %s.png)%s", label, shortImageName(a.info), a.info.Package, why))
			case kind != "png":
				p.convertedIcons = append(p.convertedIcons, fmt.Sprintf("%s (%s)", label, kind))
				if len(notes) > 0 {
					p.partialIcons = append(p.partialIcons, fmt.Sprintf("%s: %s not drawn; to replace it add images/%s",
						label, strings.Join(notes, ", "), shortImageName(a.info)))
				}
			}
		}
	}
	for _, c := range custom {
		if !used[c] {
			p.unusedImages = append(p.unusedImages, "images/"+c)
		}
	}
	for _, l := range [][]string{p.missingIcons, p.convertedIcons, p.partialIcons, p.sameCustom, p.differentCustom, p.unusedImages} {
		sortFold(l)
	}
	return nil
}

// planOutputCleanup lists everything in the update folder that is not a
// listed version folder or repositories.inf. Hidden files (e.g. .keep) are
// left alone.
func (p *repoPlan) planOutputCleanup() error {
	entries, err := os.ReadDir(p.cfg.Output)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		n := e.Name()
		switch {
		case n == repositoriesInfFilename:
		case e.IsDir() && slices.Contains(p.cfg.Versions, n):
		case e.IsDir() && isLeftoverFolder(n):
			// Leftovers of an interrupted run are cleaned up when executing;
			// a .old folder that a version is restored from is not one
			v := strings.TrimSuffix(strings.TrimSuffix(n[1:], ".old"), ".tmp")
			if strings.HasSuffix(n, ".old") && slices.Contains(p.cfg.Versions, v) {
				if _, err := os.Stat(filepath.Join(p.cfg.Output, v)); err != nil {
					continue
				}
			}
			p.leftoverDirs = append(p.leftoverDirs, n)
		case strings.HasPrefix(n, "."):
			// other hidden files (.keep) stay
		default:
			p.remove = append(p.remove, n)
		}
	}
	return nil
}

// sortedApks returns the apks of a version sorted by display name, ATAK first.
func (vp *versionPlan) sortedApks() []*repoApk {
	var list []*repoApk
	for _, a := range vp.apks {
		list = append(list, a)
	}
	slices.SortFunc(list, func(a, b *repoApk) int {
		return compareAtakFirst(a.info.Package, a.info.DisplayName, b.info.Package, b.info.DisplayName)
	})
	return list
}

// isLeftoverFolder reports whether name is a folder that an interrupted run
// left: .<version>.tmp or .<version>.old.
func isLeftoverFolder(name string) bool {
	for _, suffix := range []string{".tmp", ".old"} {
		if strings.HasPrefix(name, ".") && strings.HasSuffix(name, suffix) &&
			versionRe.MatchString(strings.TrimSuffix(name[1:], suffix)) {
			return true
		}
	}
	return false
}
