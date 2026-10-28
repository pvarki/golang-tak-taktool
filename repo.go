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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

func (a *repoApk) revision() int64 {
	r, _ := strconv.ParseInt(a.info.Revision, 10, 64)
	return r
}

// leftover is an apk that stays in the apk folder.
type leftover struct {
	file   string
	reason string
	// removable is true if the apk is certainly not needed (a copy of a
	// distributed apk or of another apk, a Git LFS pointer); -clean removes
	// these. Older revisions are not: the newer one may be a special build.
	removable bool
	// expected is true if the apk is left on purpose (excluded, not in
	// packages, the other ATAK variant)
	expected bool
	// otherAtak is the ATAK version the apk is made for, when it is not in
	// versions
	otherAtak string
}

// quiet reports whether the leftover is only listed with -v: left on
// purpose or certainly not needed.
func (l leftover) quiet() bool { return l.expected || l.removable }

type versionPlan struct {
	version string
	exists  bool // the version folder exists already
	// restoring is true if the version folder is missing but an interrupted
	// run left it as .<version>.old; the folder is rebuilt from there
	restoring bool
	apks      map[string]*repoApk // package -> apk to distribute
	added     []string
	updated   []string
	removed   []string
	other     []string // e.g. renamed apks, rebuilt product.infz
}

func (vp *versionPlan) hasChanges() bool {
	return len(vp.added)+len(vp.updated)+len(vp.removed)+len(vp.other) > 0 || !vp.exists || vp.restoring
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
	// icons drawn without the parts taktool cannot draw (strokes, ...)
	partialIcons []string
	unusedImages []string // images/ files that no distributed app uses

	templates []templateApk // plugins with the plugin template's package name

	manuals     []manualFile // manuals to add or update in the manuals folder
	manualNotes []string     // other PDFs that are not copied

	removedFolder []removedEntry // apks in apks/removed/
	// version -> packages whose newest revision has two different apks in
	// the apk folder; -clean leaves the other apks of these packages alone
	conflicts map[string]map[string]bool
	// apks excluded by file name, to warn if the same apk is distributed
	excludedFiles []excludedFile
	leftoverDirs  []string // .<version>.tmp/.old folders of an interrupted run
	cleaned       int      // apks removed by -clean/-cleanall when executing
}

// templateApk is a plugin that is not distributed because it uses the
// package name of ATAK's plugin template.
type templateApk struct {
	name, pkg string
	version   string // the version folder it is taken out of, or "" (apk folder)
}

// excludedFile is an apk of the apk folder that is excluded by its file name.
type excludedFile struct {
	file, pattern string
	info          ApkInfo
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
	// otherAtak is the ATAK version the apk is made for, if that version is
	// not in versions (the report counts these by version)
	otherAtak string
}

type repoOptions struct {
	config   string
	dryRun   bool
	clean    bool // -clean
	cleanAll bool // -cleanall: remove every apk that is not distributed
	yes      bool // apply without asking
	verbose  bool
}

// cleanMode says which apks of the apk folder are removed.
type cleanMode int

const (
	cleanNone cleanMode = iota
	cleanSafe           // -clean: apks that are certainly not needed
	cleanAll            // -cleanall: every apk that is not distributed
)

func (o repoOptions) cleanMode() cleanMode {
	switch {
	case o.cleanAll:
		return cleanAll
	case o.clean:
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
	// Reading the apks of the removed folder is only needed for -clean
	if err := plan.planRemovedFolder(o.cleanMode() == cleanSafe); err != nil {
		return err
	}

	printRepoReport(out, plan, reportOptions{dryRun: o.dryRun, clean: o.cleanMode(), verbose: o.verbose})
	if o.dryRun {
		return nil
	}
	updateWork := plan.hasUpdateWork()
	manualsWork := len(plan.manuals) > 0
	cleanCount := plan.cleanCount(o.cleanMode()) + plan.cleanRemovedCount(o.cleanMode())
	if !updateWork && !manualsWork && cleanCount == 0 {
		fmt.Fprintln(out, "\nNothing to do.")
		return nil
	}
	if !o.yes {
		var folders []string
		if updateWork {
			folders = append(folders, cfg.Output)
		}
		if manualsWork {
			folders = append(folders, cfg.Manuals.Folder)
		}
		var question string
		switch {
		case len(folders) > 0 && cleanCount > 0:
			question = fmt.Sprintf("Apply these changes to %s\nand remove %d apks from %s?", strings.Join(folders, " and "), cleanCount, cfg.Dir)
		case len(folders) > 0:
			question = fmt.Sprintf("Apply these changes to %s?", strings.Join(folders, " and "))
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
	notes, err := executeRepoPlan(plan, o.cleanMode(), log)
	if len(notes) > 0 {
		fmt.Fprintln(out, "\nNotes:")
		for _, n := range notes {
			fmt.Fprintf(out, "  %s\n", n)
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	if updateWork {
		fmt.Fprintln(out, "Update folder ready:", cfg.Output)
	} else {
		fmt.Fprintln(out, "Update folder up to date:", cfg.Output)
	}
	if manualsWork {
		fmt.Fprintf(out, "Manuals copied: %d to %s\n", len(plan.manuals), cfg.Manuals.Folder)
	}
	if plan.cleaned > 0 {
		fmt.Fprintf(out, "Removed %d apks from %s\n", plan.cleaned, cfg.Dir)
	}
	return nil
}

// removesApks reports whether apks leave the update folder.
func (p *repoPlan) removesApks() bool {
	for _, vp := range p.versions {
		if len(vp.removed)+len(vp.updated) > 0 {
			return true
		}
	}
	for _, n := range p.remove {
		if versionRe.MatchString(n) {
			return true
		}
	}
	return false
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

// hasUpdateWork reports whether executing the plan would change the update
// folder.
func (p *repoPlan) hasUpdateWork() bool {
	if len(p.remove) > 0 || len(p.moved) > 0 || len(p.leftoverDirs) > 0 {
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
	return false
}

// confirm asks a yes/no question. If there is no answer at all (standard
// input is empty or closed, e.g. in a script or cron job), it returns an error
// that tells to use -yes. Git Bash and other terminals whose input is a pipe
// work, because only a missing answer is an error.
func confirm(in io.Reader, out io.Writer, question string) (bool, error) {
	fmt.Fprint(out, question)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return false, errors.New("no answer (not running in a terminal?), nothing was changed; use -yes to apply the changes without asking")
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes" || answer == "k" || answer == "kyllä", nil
}
