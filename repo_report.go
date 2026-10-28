package main

// The report of 'taktool repo'.

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

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
	sortFold(out)
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
		if vp.restoring {
			fmt.Fprintln(w, vp.version+" (restored from an interrupted run)")
		} else if vp.exists {
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
		fmt.Fprintln(w, "Removed from the update folder (apks go to the removed folder, other files are deleted):")
		writeList(w, "    ", items)
	}
	if len(p.leftoverDirs) > 0 {
		if !changed {
			fmt.Fprintln(w, "Changes:")
			changed = true
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Cleaned up, left by an interrupted run (apks that are not in the version folder go to the removed folder):")
		writeList(w, "    ", p.leftoverDirs)
	}
	if !changed {
		fmt.Fprintln(w, "Changes: none")
	} else if p.removesApks() {
		fmt.Fprintf(w, "\nRemoved and replaced apks are moved to %s", shortRemovedDir(p.cfg))
		if o.clean != cleanNone {
			fmt.Fprint(w, " (not removed in this run)")
		}
		fmt.Fprintln(w, ".")
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

	// Without -v: apks made for other ATAK versions are counted per version
	// on one line, apks left on purpose on another
	var attention, expected, otherAtak []leftover
	for _, l := range p.leftovers {
		switch {
		case l.quiet():
			expected = append(expected, l)
		case l.otherAtak != "" && !o.verbose:
			otherAtak = append(otherAtak, l)
		default:
			attention = append(attention, l)
		}
	}
	shown := attention
	if o.verbose {
		shown = p.leftovers
	}
	if len(shown) > 0 || len(expected) > 0 || len(otherAtak) > 0 {
		if !o.verbose && len(expected)+len(otherAtak) > 0 {
			fmt.Fprintln(w, "\nLeft in the apk folder (-v lists them):")
		} else {
			fmt.Fprintln(w, "\nLeft in the apk folder:")
		}
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
		if len(otherAtak) > 0 {
			count := map[string]int{}
			var vs []string
			for _, l := range otherAtak {
				if count[l.otherAtak] == 0 {
					vs = append(vs, l.otherAtak)
				}
				count[l.otherAtak]++
			}
			slices.SortFunc(vs, compareVersions)
			var parts []string
			for _, v := range vs {
				parts = append(parts, fmt.Sprintf("%s: %d", v, count[v]))
			}
			msg := fmt.Sprintf("  %d for ATAK versions not in versions (%s)", len(otherAtak), strings.Join(parts, ", "))
			if o.clean == cleanAll {
				msg += "; -cleanall removes them"
			}
			fmt.Fprintln(w, msg)
		}
		if !o.verbose && len(expected) > 0 {
			removable := 0
			for _, l := range expected {
				if l.removable {
					removable++
				}
			}
			msg := fmt.Sprintf("  %d left on purpose or not needed (excluded, other ATAK variant, older, copies)", len(expected))
			switch {
			case o.clean == cleanAll:
				msg += "; -cleanall removes them"
			case removable > 0 && o.clean == cleanSafe:
				msg += fmt.Sprintf("; -clean removes %d", removable)
			case removable > 0:
				msg += fmt.Sprintf("; -clean would remove %d", removable)
			}
			fmt.Fprintln(w, msg)
		}
	}

	if n := len(p.removedFolder); n > 0 {
		msg := fmt.Sprintf("\n%s: %d apks taken out of the update folder", shortRemovedDir(p.cfg), n)
		switch c := p.cleanRemovedCount(o.clean); {
		case o.clean == cleanAll:
			msg += "; -cleanall removes them:"
		case o.clean == cleanSafe && c > 0:
			msg += fmt.Sprintf("; -clean removes %d:", c)
		case o.clean == cleanSafe:
			msg += "; -clean removes none of them (none is replaced or distributed)"
		default:
			msg += "; -clean removes the ones that are replaced or distributed, -cleanall all"
		}
		fmt.Fprintln(w, msg)
		// Every apk removed from there is named first
		for _, r := range p.removedFolder {
			switch {
			case o.clean == cleanAll:
				fmt.Fprintf(w, "  %s\n", r.file)
			case o.clean == cleanSafe && r.removable:
				fmt.Fprintf(w, "  %s: %s\n", r.file, r.reason)
			}
		}
	}

	printManuals(w, p, o.verbose, false)

	printIconNotes(w, p, o.verbose)

	if len(p.templates) > 0 {
		var names []string
		for _, t := range p.templates {
			n := t.name
			if t.version != "" {
				n += " (taken out of " + t.version + ")"
			}
			names = append(names, n)
		}
		fmt.Fprintln(w, "\nNot distributed, they use the plugin template's package name:")
		writeList(w, "    ", names)
		if o.verbose {
			fmt.Fprintln(w, "  ATAK cannot tell these from other plugins built from the template (package")
			fmt.Fprintln(w, "  com.atakmap.android.plugintemplate.plugin); ask the developer for a build with its own package name.")
		}
	}

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

// printIconNotes prints the icon notes: in short, or with verbose in full.
func printIconNotes(w io.Writer, p *repoPlan, verbose bool) {
	if !verbose {
		var parts []string
		if n := len(p.missingIcons); n > 0 {
			parts = append(parts, fmt.Sprintf("%d apps without an icon (empty in ATAK)", n))
		}
		if n := len(p.partialIcons); n > 0 {
			parts = append(parts, fmt.Sprintf("%d partly drawn", n))
		}
		if n := len(p.differentCustom); n > 0 {
			parts = append(parts, fmt.Sprintf("%d images/ files differ from the apk's icon", n))
		}
		if n := len(p.sameCustom); n > 0 {
			parts = append(parts, fmt.Sprintf("%d images/ files not needed", n))
		}
		if n := len(p.unusedImages); n > 0 {
			parts = append(parts, fmt.Sprintf("%d images/ files unused", n))
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
	section("Icons drawn without some of their parts (they may look different in ATAK):", p.partialIcons)
	section("Custom image differs from the apk's own icon (which may be newer); remove it to use the apk's icon:", p.differentCustom)
	section("A custom image is the same as the apk's own icon and is not needed:", p.sameCustom)
	if len(p.unusedImages) > 0 {
		fmt.Fprintln(w, "\nNot used by any distributed app (kept for apps that may come later):")
		writeList(w, "    ", p.unusedImages)
	}
	if len(p.convertedIcons) > 0 {
		fmt.Fprintln(w, "\nIcons converted to png:")
		writeList(w, "    ", p.convertedIcons)
	}
}
