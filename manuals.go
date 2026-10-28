package main

// Manuals: taktool repo can copy the PDF manuals bundled in the distributed
// apks into a flat folder (manuals in taktool.yaml). The folder must not be
// inside the update folder, which TAK Server serves to every device.
//
// Plugins built with the ATAK plugin SDK have their manual at
// assets/usermanual.pdf; ATAK itself has its documents under
// assets/support/docs/. These are named
//
//	<Product> <TAK version> <Plugin> <plugin version> <DocType>.pdf
//
// e.g. "ATAK 5.8 VNS 4.0 UserGuide.pdf", "ATAK 5.8 UserGuide.pdf". Other PDFs
// in an apk may be outdated (e.g. Data Sync 5.0.2 also bundles the guide of
// version 3.5), so they are only reported, or copied with their own name when
// manuals: all: true is set.

import (
	"archive/zip"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

const pluginManualEntry = "assets/usermanual.pdf"
const atakDocsPrefix = "assets/support/docs/"

// manualFile is a PDF to copy from an apk into the manuals folder.
type manualFile struct {
	name  string // file name in the manuals folder
	apk   string // apk path
	entry string // path inside the apk
	crc   uint32
	size  uint64
	state string // "add" or "update"
}

// compactName joins the words of a name: "Data Sync" -> "DataSync".
func compactName(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// majorMinor returns "5.8" from "5.8.0".
func majorMinor(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return v
}

// docType makes a document type from a file name:
// "ATAK_User_Guide.pdf" -> "UserGuide", "ATAK_Icon_Glossary.pdf" -> "IconGlossary".
func docType(fileName string) string {
	base := strings.TrimSuffix(fileName, path.Ext(fileName))
	words := strings.FieldsFunc(base, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	if len(words) > 1 && strings.EqualFold(words[0], "ATAK") {
		words = words[1:]
	}
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, "")
}

// safeFileName removes characters that are not allowed in Windows file names.
func safeFileName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// manualsInApk lists the manuals of an apk distributed for ATAK version v.
// others are the PDFs that are not copied unless all is set.
func manualsInApk(a *repoApk, v string, all bool) (manuals []manualFile, others []string, err error) {
	zr, err := zip.OpenReader(a.path)
	if err != nil {
		return nil, nil, err
	}
	defer zr.Close()

	atak := isAtakApp(a.info.Package)
	prefix := "ATAK " + majorMinor(v)
	if !atak {
		prefix += " " + compactName(a.info.DisplayName) + " " + shortVersion(a.info.Version)
	}

	for _, f := range zr.File {
		if !strings.EqualFold(path.Ext(f.Name), ".pdf") {
			continue
		}
		var name string
		switch {
		case !atak && f.Name == pluginManualEntry:
			name = prefix + " UserGuide.pdf"
		case atak && strings.HasPrefix(f.Name, atakDocsPrefix) && !strings.Contains(strings.TrimPrefix(f.Name, atakDocsPrefix), "/"):
			name = prefix + " " + docType(path.Base(f.Name)) + ".pdf"
		case all:
			// Own name, without versions that might be wrong for this file
			product := "ATAK"
			if !atak {
				product = compactName(a.info.DisplayName)
			}
			name = product + " " + path.Base(f.Name)
		default:
			others = append(others, path.Base(f.Name))
			continue
		}
		manuals = append(manuals, manualFile{
			name:  safeFileName(name),
			apk:   a.path,
			entry: f.Name,
			crc:   f.CRC32,
			size:  f.UncompressedSize64,
		})
	}
	return manuals, others, nil
}

// planManuals lists the manuals of the apks that will be distributed and
// which of them are new or changed in the manuals folder.
func (p *repoPlan) planManuals() error {
	cfg := p.cfg
	if cfg.Manuals.Folder == "" {
		return nil
	}

	// The same apk (e.g. HelmCam, which does not depend on the ATAK version)
	// can be distributed to several versions. Its manuals are copied once,
	// named after the highest of those versions.
	type use struct {
		apk     *repoApk
		version string
	}
	var order []string
	latest := map[string]use{} // apk content hash -> highest version
	for _, vp := range p.versions {
		for _, a := range vp.sortedApks() {
			key := a.info.Hash
			if key == "" {
				key = a.path
			}
			u, seen := latest[key]
			if !seen {
				order = append(order, key)
			}
			if !seen || compareVersions(vp.version, u.version) > 0 {
				latest[key] = use{a, vp.version}
			}
		}
	}

	seen := map[string]bool{}
	others := map[string][]string{} // package label -> other PDFs, listed once
	var labels []string
	for _, key := range order {
		u := latest[key]
		manuals, other, err := manualsInApk(u.apk, u.version, cfg.Manuals.All)
		if err != nil {
			p.warn("%s: cannot read manuals: %v", filepath.Base(u.apk.path), err)
			continue
		}
		label := u.apk.info.DisplayName
		for _, o := range other {
			if _, ok := others[label]; !ok {
				labels = append(labels, label)
			}
			if !slices.Contains(others[label], o) {
				others[label] = append(others[label], o)
			}
		}
		for _, m := range manuals {
			k := strings.ToLower(m.name)
			if seen[k] {
				continue
			}
			seen[k] = true
			state, err := manualState(filepath.Join(cfg.Manuals.Folder, m.name), m)
			if err != nil {
				return err
			}
			if state != "" {
				m.state = state
				p.manuals = append(p.manuals, m)
			}
		}
	}

	sortFold(labels)
	for _, l := range labels {
		p.manualNotes = append(p.manualNotes, fmt.Sprintf("%s: other PDFs not copied (set manuals: all: true to copy them): %s",
			l, strings.Join(others[l], ", ")))
	}
	slices.SortFunc(p.manuals, func(a, b manualFile) int { return strings.Compare(a.name, b.name) })
	return nil
}

// manualState returns "add" if the file does not exist, "update" if its
// content differs, or "" if it is up to date.
func manualState(dst string, m manualFile) (string, error) {
	st, err := os.Stat(dst)
	if os.IsNotExist(err) {
		return "add", nil
	}
	if err != nil {
		return "", err
	}
	if uint64(st.Size()) != m.size {
		return "update", nil
	}
	f, err := os.Open(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if h.Sum32() != m.crc {
		return "update", nil
	}
	return "", nil
}

// writeManuals copies the planned manuals into the manuals folder.
func writeManuals(dir string, manuals []manualFile) error {
	if len(manuals) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, m := range manuals {
		if err := writeManual(dir, m); err != nil {
			return fmt.Errorf("manual %s: %w", m.name, err)
		}
	}
	return nil
}

func writeManual(dir string, m manualFile) (err error) {
	dst := filepath.Join(dir, m.name)
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			out.Close()
			os.Remove(tmp)
		}
	}()
	if _, err = copyFileFromZip(out, m.apk, m.entry); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
