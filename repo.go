package main

// The repo command builds TAK Server's update folder for several ATAK
// versions at once:
//
//	update/
//	├── repositories.inf     ATAK versions the server distributes, one per
//	│                        line in ascending order, e.g. "5.8.0". TAK
//	│                        Server/ATAK reads this file.
//	├── 5.6.0/
//	│   ├── atak_app.apk
//	│   ├── vns_plugin.apk
//	│   └── product.infz     zip with product.inf and the icons; tells ATAK
//	│                        which apps and plugins are offered for 5.6.0
//	└── 5.8.0/
//	    └── ...
//
// The apks are taken from the apk folder (where taktool.yaml is) and sorted
// into the version folders by the ATAK version they are made for. Version
// folders are updated incrementally: apks already in the update folder stay
// unless a newer revision of the same package replaces them.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const repositoriesInfFilename = "repositories.inf"

// repoApk is an apk in the apk folder or in a version folder.
type repoApk struct {
	info    ApkInfo
	path    string // where the file is now
	target  atakTarget
	variant string // ATAK only: small or full
}

func (a *repoApk) name() string {
	n := a.info.DisplayName + " " + shortVersion(a.info.Version)
	if a.variant != "" {
		n += " (" + a.variant + ")"
	}
	return n
}

// setVariant detects the ATAK variant of an ATAK apk.
func (p *repoPlan) setVariant(a *repoApk) {
	if !isAtakApp(a.info.Package) {
		return
	}
	v, warning, err := detectAtakVariant(a.path, a.info)
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

func (a *repoApk) revision() int64 {
	r, _ := strconv.ParseInt(a.info.Revision, 10, 64)
	return r
}

// leftover is an apk that stays in the apk folder.
type leftover struct {
	file   string
	reason string
	// removable is true if the apk is certainly not needed (an older
	// revision, a copy of a distributed apk, a Git LFS pointer); -clean
	// removes these.
	removable bool
	// expected is true if the apk is left on purpose (excluded, not in
	// packages, the other ATAK variant) or certainly not needed; these are
	// only listed with -v
	expected bool
}

type versionPlan struct {
	version string
	exists  bool                // the version folder exists already
	apks    map[string]*repoApk // package -> apk to distribute
	added   []string
	updated []string
	removed []string
	other   []string // e.g. renamed apks, rebuilt product.infz
}

func (vp *versionPlan) hasChanges() bool {
	return len(vp.added)+len(vp.updated)+len(vp.removed)+len(vp.other) > 0 || !vp.exists
}

type repoPlan struct {
	cfg       *Config
	versions  []*versionPlan
	remove    []string // entries in the update folder to delete
	leftovers []leftover
	moved     []string // apks in the apk folder that are moved into version folders
	warnings  []string

	missingIcons []string // apps without a usable icon and without a custom image
	// notes about icons: icons converted to png, custom images that are the
	// same as the apk's own icon or differ from it
	convertedIcons, sameCustom, differentCustom []string

	manuals     []manualFile // manuals to add or update in the manuals folder
	manualNotes []string     // other PDFs that are not copied
}

func (p *repoPlan) warn(format string, args ...any) {
	p.warnings = append(p.warnings, fmt.Sprintf(format, args...))
}

// sourceResult records what happened to an apk of the apk folder.
type sourceResult struct {
	used      bool
	reason    string
	removable bool
	expected  bool
}

type repoOptions struct {
	config              string
	dryRun, cleanSource bool
	cleanAll            bool // remove every apk that is not distributed
	yes                 bool // apply without asking
	verbose             bool
}

// cleanMode says which apks of the apk folder are removed.
type cleanMode int

const (
	cleanNone cleanMode = iota
	cleanSafe           // -clean: apks that are certainly not needed
	cleanAll            // -cleanall: every apk that is not distributed
)

func (o repoOptions) clean() cleanMode {
	switch {
	case o.cleanAll:
		return cleanAll
	case o.cleanSource:
		return cleanSafe
	}
	return cleanNone
}

// removedBy reports whether the clean mode removes the leftover apk.
func (l leftover) removedBy(m cleanMode) bool {
	return m == cleanAll || (m == cleanSafe && l.removable)
}

// errNotConfirmed is returned when the user does not confirm the changes.
var errNotConfirmed = errors.New("cancelled, nothing was changed")

func PackageRepository(o repoOptions, in io.Reader, out io.Writer) error {
	cfg, err := loadConfig(o.config)
	if err != nil {
		return err
	}
	plan, err := planRepository(cfg)
	if err != nil {
		return err
	}

	printRepoReport(out, plan, reportOptions{dryRun: o.dryRun, clean: o.clean(), verbose: o.verbose})
	if o.dryRun {
		return nil
	}
	updateWork := plan.hasWork(cleanNone)
	cleanCount := plan.cleanCount(o.clean())
	if !updateWork && cleanCount == 0 {
		fmt.Fprintln(out, "\nNothing to do.")
		return nil
	}
	if !o.yes {
		var question string
		switch {
		case updateWork && cleanCount > 0:
			question = fmt.Sprintf("Apply these changes to %s\nand remove %d apks from %s?", cfg.Output, cleanCount, cfg.Dir)
		case updateWork:
			question = fmt.Sprintf("Apply these changes to %s?", cfg.Output)
		default:
			question = fmt.Sprintf("The update folder is up to date. Remove %d apks from %s?", cleanCount, cfg.Dir)
		}
		ok, err := confirm(in, out, "\n"+question+
			"\nIf you are not sure, answer n and run 'taktool repo -dry-run -v' first. [y/N]: ")
		if err != nil {
			return err
		}
		if !ok {
			return errNotConfirmed
		}
	}

	var log io.Writer = io.Discard
	if o.verbose {
		log = &uniqueLines{w: out}
	}
	if err := executeRepoPlan(plan, o.clean(), log); err != nil {
		return err
	}
	fmt.Fprintln(out)
	if updateWork {
		fmt.Fprintln(out, "Update folder ready:", cfg.Output)
	} else {
		fmt.Fprintln(out, "Update folder up to date:", cfg.Output)
	}
	if cleanCount > 0 {
		fmt.Fprintf(out, "Removed %d apks from %s\n", cleanCount, cfg.Dir)
	}
	return nil
}

// cleanCount returns the number of apks the clean mode removes.
func (p *repoPlan) cleanCount(m cleanMode) int {
	n := 0
	for _, l := range p.leftovers {
		if l.removedBy(m) {
			n++
		}
	}
	return n
}

// hasWork reports whether executing the plan would change anything.
func (p *repoPlan) hasWork(clean cleanMode) bool {
	if len(p.remove) > 0 || len(p.moved) > 0 || len(p.manuals) > 0 {
		return true
	}
	for _, vp := range p.versions {
		if vp.hasChanges() {
			return true
		}
	}
	if _, err := os.Stat(filepath.Join(p.cfg.Output, repositoriesInfFilename)); err != nil {
		return true
	}
	return p.cleanCount(clean) > 0
}

// confirm asks a yes/no question. When in is the standard input and it is
// not a terminal (e.g. a script or cron job), it returns an error instead of
// waiting for an answer: use -yes there.
func confirm(in io.Reader, out io.Writer, question string) (bool, error) {
	if f, ok := in.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice == 0 {
			return false, errors.New("not running in a terminal, so the changes cannot be confirmed; use -yes to apply them without asking")
		}
	}
	fmt.Fprint(out, question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes" || answer == "k" || answer == "kyllä", nil
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
		if n == repositoriesInfFilename || n == "repository.inf" || (e.IsDir() && versionRe.MatchString(n)) {
			return nil
		}
	}
	return fmt.Errorf("%s does not look like an update folder (no repositories.inf or version folders); "+
		"refusing to delete its contents. Check 'output' in %s", out, configFileName)
}

func planRepository(cfg *Config) (*repoPlan, error) {
	if err := checkOutputFolder(cfg); err != nil {
		return nil, err
	}
	plan := &repoPlan{cfg: cfg}

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
				removable: r.removable, expected: r.expected || r.removable})
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
		candidates[v] = p.newestPerPackage(list, results)
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
// the apk folder.
func (p *repoPlan) newestPerPackage(list []*repoApk, results map[string]*sourceResult) []*repoApk {
	byPkg := map[string]*repoApk{}
	var order []string
	for _, a := range list {
		b, seen := byPkg[a.info.Package]
		if !seen {
			byPkg[a.info.Package] = a
			order = append(order, a.info.Package)
			continue
		}
		if b == nil {
			// An earlier conflict for this package
			setResult(results, a.path, "another apk of the same package has the same revision but different content", false)
			continue
		}
		switch {
		case a.revision() > b.revision():
			setResult(results, b.path, fmt.Sprintf("older (revision %s) than %s (revision %s) in the apk folder",
				b.info.Revision, filepath.Base(a.path), a.info.Revision), true)
			byPkg[a.info.Package] = a
		case a.revision() < b.revision():
			setResult(results, a.path, fmt.Sprintf("older (revision %s) than %s (revision %s) in the apk folder",
				a.info.Revision, filepath.Base(b.path), b.info.Revision), true)
		case a.info.Hash == b.info.Hash:
			setResult(results, a.path, fmt.Sprintf("copy of %s", filepath.Base(b.path)), true)
		default:
			p.warn("%s and %s have the same revision %s but different content; neither was distributed",
				filepath.Base(a.path), filepath.Base(b.path), a.info.Revision)
			setResult(results, a.path, "same revision as "+filepath.Base(b.path)+" but different content", false)
			setResult(results, b.path, "same revision as "+filepath.Base(a.path)+" but different content", false)
			byPkg[a.info.Package] = nil
		}
	}
	var out []*repoApk
	for _, pkg := range order {
		if a := byPkg[pkg]; a != nil {
			out = append(out, a)
		}
	}
	return out
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
	cfg := p.cfg
	vp := &versionPlan{version: v, apks: map[string]*repoApk{}}
	dir := filepath.Join(cfg.Output, v)

	// Apks already in the version folder
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		vp.exists = true
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() || !isApkFile(e.Name()) {
			if e.Name() != productInfzFilename {
				vp.removed = append(vp.removed, e.Name())
			}
			continue
		}
		if lfs, err := isLFSPointer(path); err != nil {
			return nil, err
		} else if lfs {
			p.warn("%s/%s is a Git LFS pointer, not an apk; removed", v, e.Name())
			vp.removed = append(vp.removed, e.Name()+" (Git LFS pointer)")
			continue
		}
		info, err := getApkData(path)
		if err != nil {
			p.warn("%s/%s cannot be read (%v); removed", v, e.Name(), err)
			vp.removed = append(vp.removed, e.Name()+" (cannot read)")
			continue
		}
		a := &repoApk{info: info, path: path}
		p.setVariant(a)
		if !cfg.wantsPackage(info.Package) {
			vp.removed = append(vp.removed, a.name()+" (not in packages)")
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

	// Merge the apks of the apk folder: the newest revision wins (for ATAK,
	// the variant set in taktool.yaml first)
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
			setResult(results, c.path, fmt.Sprintf("older (revision %s) than %s (revision %s) in %s",
				c.info.Revision, e.name(), e.info.Revision, v), true)
		case c.info.Hash == e.info.Hash:
			setResult(results, c.path, fmt.Sprintf("already in %s", v), true)
		default:
			p.warn("%s: %s has the same revision %s as the apk in %s but different content; kept the one in %s",
				filepath.Base(c.path), c.info.DisplayName, c.info.Revision, v, v)
			setResult(results, c.path, fmt.Sprintf("same revision as in %s but different content", v), false)
		}
	}

	// File names must be unique (case-insensitively)
	names := map[string]*repoApk{}
	for _, a := range vp.apks {
		n := strings.ToLower(preferredApkName(a.info))
		if b, dup := names[n]; dup {
			return nil, fmt.Errorf("%s: %s (%s) and %s (%s) would both be named %s",
				v, a.info.DisplayName, a.info.Package, b.info.DisplayName, b.info.Package, preferredApkName(a.info))
		}
		names[n] = a
	}

	// An existing version folder may need to be rebuilt even if no apk
	// changed: apks with other names, or a product.infz written by an older
	// version of taktool or with other icons
	if vp.exists && !vp.hasChanges() {
		for _, a := range vp.sortedApks() {
			if want := preferredApkName(a.info); filepath.Base(a.path) != want {
				vp.other = append(vp.other, fmt.Sprintf("rename %s -> %s", filepath.Base(a.path), want))
			}
		}
		same, err := p.productInfzUpToDate(vp, dir)
		if err != nil {
			return nil, err
		}
		if !same {
			vp.other = append(vp.other, "rebuild "+productInfzFilename)
		}
	}

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
	return vp, nil
}

// productInfzUpToDate reports whether dir/product.infz is exactly what
// building the version folder would write.
func (p *repoPlan) productInfzUpToDate(vp *versionPlan, dir string) (bool, error) {
	existing, err := os.ReadFile(filepath.Join(dir, productInfzFilename))
	if err != nil {
		return false, nil
	}
	tmp, err := os.MkdirTemp("", "taktool-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	if err := writeProductInfz(tmp, vp.productInfos(), filepath.Join(p.cfg.Dir, "images"), io.Discard); err != nil {
		return false, err
	}
	want, err := os.ReadFile(filepath.Join(tmp, productInfzFilename))
	if err != nil {
		return false, err
	}
	return bytes.Equal(existing, want), nil
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
	custom, err := checkForCustomImages(imagesDir)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, vp := range p.versions {
		for _, a := range vp.sortedApks() {
			if seen[a.info.Package] {
				continue
			}
			seen[a.info.Package] = true
			name := preferredApkName(a.info)
			c := customImageFor(a.info, strings.TrimSuffix(name, filepath.Ext(name))+".png", custom)
			own, kind, iconErr := apkIcon(a.path, a.info.IconPath)
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
				p.missingIcons = append(p.missingIcons, fmt.Sprintf("%s: images/%s (or %s.png)", label, shortImageName(a.info), a.info.Package))
			case kind != "png":
				p.convertedIcons = append(p.convertedIcons, fmt.Sprintf("%s (%s)", label, kind))
			}
		}
	}
	for _, l := range [][]string{p.missingIcons, p.convertedIcons, p.sameCustom, p.differentCustom} {
		slices.SortFunc(l, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
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
		case strings.HasPrefix(n, "."):
			// Leftovers of an interrupted run are cleaned up when executing
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
		if isAtakApp(a.info.Package) != isAtakApp(b.info.Package) {
			if isAtakApp(a.info.Package) {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.info.DisplayName), strings.ToLower(b.info.DisplayName))
	})
	return list
}

// reportOptions controls what printRepoReport prints.
type reportOptions struct {
	dryRun  bool
	clean   cleanMode
	verbose bool // also the summary, expected leftovers and notes
}

// writeList writes items separated by commas, wrapped at about 100 columns,
// each line indented by indent.
func writeList(w io.Writer, indent string, items []string) {
	line := indent
	for i, it := range items {
		if i < len(items)-1 {
			it += ","
		}
		if line != indent && len(line)+1+len(it) > 100 {
			fmt.Fprintln(w, line)
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += it
	}
	fmt.Fprintln(w, line)
}

func sortedCopy(items []string) []string {
	out := slices.Clone(items)
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

func printRepoReport(w io.Writer, p *repoPlan, o reportOptions) {
	if o.dryRun {
		fmt.Fprintln(w, "Dry run, nothing is changed.")
		fmt.Fprintln(w)
	}

	changed := false
	for _, vp := range p.versions {
		if !vp.hasChanges() {
			continue
		}
		if !changed {
			fmt.Fprintln(w, "Changes:")
			changed = true
		}
		fmt.Fprintln(w)
		if vp.exists {
			fmt.Fprintln(w, vp.version)
		} else {
			fmt.Fprintln(w, vp.version+" (new version folder)")
		}
		for _, g := range []struct {
			title string
			items []string
		}{{"Added", vp.added}, {"Updated", vp.updated}, {"Removed", vp.removed}, {"Other", vp.other}} {
			if len(g.items) > 0 {
				fmt.Fprintf(w, "  %s:\n", g.title)
				writeList(w, "    ", sortedCopy(g.items))
			}
		}
	}
	if len(p.remove) > 0 {
		if !changed {
			fmt.Fprintln(w, "Changes:")
			changed = true
		}
		var items []string
		for _, n := range p.remove {
			if versionRe.MatchString(n) {
				n += " (not in versions)"
			}
			items = append(items, n)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Removed from the update folder:")
		writeList(w, "    ", items)
	}
	if !changed {
		fmt.Fprintln(w, "Changes: none")
	}

	if o.verbose {
		fmt.Fprintln(w, "\nSummary:")
		for _, vp := range p.versions {
			var names []string
			for _, a := range vp.sortedApks() {
				names = append(names, a.name())
			}
			if len(names) == 0 {
				names = []string{"(nothing)"}
			}
			fmt.Fprintln(w)
			fmt.Fprintln(w, vp.version)
			writeList(w, "    ", names)
		}
	}

	var attention, expected []leftover
	for _, l := range p.leftovers {
		if l.expected {
			expected = append(expected, l)
		} else {
			attention = append(attention, l)
		}
	}
	shown := attention
	if o.verbose {
		shown = p.leftovers
	}
	if len(shown) > 0 || len(expected) > 0 {
		fmt.Fprintln(w, "\nLeft in the apk folder:")
		for _, l := range shown {
			mark := ""
			switch {
			case o.clean == cleanAll:
				mark = " [removed by -cleanall]"
			case l.removable && o.clean == cleanSafe:
				mark = " [removed by -clean]"
			case l.removable:
				mark = " [-clean would remove]"
			}
			fmt.Fprintf(w, "  %s: %s%s\n", l.file, l.reason, mark)
		}
		if !o.verbose && len(expected) > 0 {
			removable := 0
			for _, l := range expected {
				if l.removable {
					removable++
				}
			}
			msg := fmt.Sprintf("  %d apks left as expected (excluded, the other ATAK variant, older or already distributed); -v lists them", len(expected))
			switch {
			case o.clean == cleanAll:
				msg += ". -cleanall removes them"
			case removable > 0 && o.clean == cleanSafe:
				msg += fmt.Sprintf(". -clean removes %d of them", removable)
			case removable > 0:
				msg += fmt.Sprintf(". -clean would remove %d of them", removable)
			}
			fmt.Fprintln(w, msg)
		}
	}

	printManuals(w, p, o.verbose, false)

	printIconNotes(w, p, o.verbose)

	if len(p.warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, s := range p.warnings {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
}

// uniqueLines writes each distinct line once (the same icon note would
// otherwise be printed for every version).
type uniqueLines struct {
	w    io.Writer
	seen map[string]bool
}

func (u *uniqueLines) Write(b []byte) (int, error) {
	if u.seen == nil {
		u.seen = map[string]bool{}
	}
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if line == "" || u.seen[line] {
			continue
		}
		u.seen[line] = true
		if _, err := io.WriteString(u.w, line); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func executeRepoPlan(p *repoPlan, clean cleanMode, log io.Writer) error {
	cfg := p.cfg
	if err := os.MkdirAll(cfg.Output, 0o755); err != nil {
		return err
	}

	// Before the version folders are rebuilt: the apks are read from where
	// they are now
	if err := writeManuals(cfg.Manuals.Folder, p.manuals); err != nil {
		return err
	}

	for _, vp := range p.versions {
		if !vp.hasChanges() {
			continue // up to date, also its product.infz
		}
		if err := buildVersionFolder(cfg, vp, log); err != nil {
			return fmt.Errorf("%s: %w", vp.version, err)
		}
	}

	if err := writeRepositoriesInf(cfg.Output, cfg.Versions); err != nil {
		return err
	}

	for _, n := range p.remove {
		if err := os.RemoveAll(filepath.Join(cfg.Output, n)); err != nil {
			return err
		}
	}

	// The apks are in the version folders now
	for _, f := range p.moved {
		if err := os.Remove(f); err != nil {
			return err
		}
	}

	for _, l := range p.leftovers {
		if l.removedBy(clean) {
			if err := os.Remove(filepath.Join(cfg.Dir, l.file)); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildVersionFolder builds the version folder in a temporary folder and
// swaps it in place when it is complete, so TAK Server never serves a half
// built folder.
func buildVersionFolder(cfg *Config, vp *versionPlan, log io.Writer) error {
	dir := filepath.Join(cfg.Output, vp.version)
	tmp := filepath.Join(cfg.Output, "."+vp.version+".tmp")
	old := filepath.Join(cfg.Output, "."+vp.version+".old")

	// Leftovers of an interrupted run
	for _, d := range []string{tmp, old} {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return err
	}

	infos := vp.productInfos()
	for i := range infos {
		if err := linkOrCopy(infos[i].srcPath, filepath.Join(tmp, infos[i].ApkPath)); err != nil {
			return err
		}
		infos[i].srcPath = ""
	}
	if err := writeProductInfz(tmp, infos, filepath.Join(cfg.Dir, "images"), log); err != nil {
		return err
	}

	if vp.exists {
		if err := os.Rename(dir, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	return os.RemoveAll(old)
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
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
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

// printIconNotes prints the icon notes: in short, or with verbose in full.
func printIconNotes(w io.Writer, p *repoPlan, verbose bool) {
	if !verbose {
		var parts []string
		if n := len(p.missingIcons); n > 0 {
			parts = append(parts, fmt.Sprintf("%d apps have no usable icon and are shown with an empty icon in ATAK", n))
		}
		if n := len(p.differentCustom); n > 0 {
			parts = append(parts, fmt.Sprintf("%d images/ files replace a different icon that the apk has (it may be newer)", n))
		}
		if n := len(p.sameCustom); n > 0 {
			parts = append(parts, fmt.Sprintf("%d images/ files are the same as the apk's own icon and not needed", n))
		}
		if len(parts) > 0 {
			fmt.Fprintf(w, "\nIcons: %s; -v lists them\n", strings.Join(parts, ", "))
		}
		return
	}
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s\n", title)
		for _, l := range lines {
			fmt.Fprintf(w, "  %s\n", l)
		}
	}
	section("No usable icon (shown empty in ATAK); add a png to the images folder:", p.missingIcons)
	section("Custom image differs from the apk's own icon (which may be newer); remove it to use the apk's icon:", p.differentCustom)
	section("A custom image is the same as the apk's own icon and is not needed:", p.sameCustom)
	if len(p.convertedIcons) > 0 {
		fmt.Fprintln(w, "\nIcons converted to png:")
		writeList(w, "    ", p.convertedIcons)
	}
}
