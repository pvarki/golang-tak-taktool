package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// productInfRow is one line of product.inf.
type productInfRow struct {
	Platform, Type, Package, DisplayName, Version, Revision string
	ApkPath, IconPath, Description, Hash                    string
	OsReq, TakReq, Size                                     string
}

type versionSummary struct {
	version string
	apps    []string
}

type checkResult struct {
	summary  []versionSummary
	errors   []string
	warnings []string
}

func (r *checkResult) errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *checkResult) warnf(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

// CheckRepository checks an update folder. cfg may be nil; if given, the
// versions and packages of taktool.yaml are checked too. It returns the
// number of errors found.
func CheckRepository(w io.Writer, dir string, cfg *Config) (int, error) {
	r, err := checkRepository(dir, cfg)
	if err != nil {
		return 0, err
	}

	fmt.Fprintln(w, "Summary:")
	for _, s := range r.summary {
		fmt.Fprintln(w)
		fmt.Fprintln(w, s.version)
		writeList(w, "    ", s.apps)
	}
	if len(r.errors) > 0 {
		fmt.Fprintln(w, "\nErrors:")
		for _, s := range r.errors {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
	if len(r.warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, s := range r.warnings {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
	if len(r.errors) == 0 && len(r.warnings) == 0 {
		fmt.Fprintln(w, "\nNo problems found.")
	}
	return len(r.errors), nil
}

func checkRepository(dir string, cfg *Config) (*checkResult, error) {
	r := &checkResult{}

	data, err := os.ReadFile(filepath.Join(dir, repositoriesInfFilename))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%s has no %s: it is not an update folder yet; run 'taktool repo' first", dir, repositoriesInfFilename)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", repositoriesInfFilename, err)
	}
	var versions []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !versionRe.MatchString(line) {
			r.errorf("%s: %q is not a version in the form 5.8.0", repositoriesInfFilename, line)
			continue
		}
		versions = append(versions, line)
	}
	if strings.HasPrefix(string(data), lfsPointerPrefix) {
		r.errorf("%s is a Git LFS pointer", repositoriesInfFilename)
	}
	if !slices.IsSortedFunc(versions, compareVersions) {
		r.warnf("%s: versions are not in ascending order", repositoriesInfFilename)
	}
	if cfg != nil && !slices.Equal(versions, cfg.Versions) {
		r.warnf("%s lists %s but %s has %s", repositoriesInfFilename,
			strings.Join(versions, ", "), configFileName, strings.Join(cfg.Versions, ", "))
	}

	// Version folders that are not listed
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() && versionRe.MatchString(e.Name()) && !slices.Contains(versions, e.Name()) {
			r.warnf("folder %s is not listed in %s", e.Name(), repositoriesInfFilename)
		}
	}

	// package -> version -> row, and version -> rows
	byPkg := map[string]map[string]productInfRow{}
	names := map[string]string{} // package -> display name
	certs := map[string]map[string][]string{}

	for _, v := range versions {
		rows := checkVersionFolder(r, dir, v)
		var summary []string
		slices.SortFunc(rows, func(a, b productInfRow) int {
			if isAtakApp(a.Package) != isAtakApp(b.Package) {
				if isAtakApp(a.Package) {
					return -1
				}
				return 1
			}
			return strings.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName))
		})
		for _, row := range rows {
			name := row.DisplayName + " " + shortVersion(row.Version)
			if isAtakApp(row.Package) {
				info := ApkInfo{Version: row.Version}
				if variant, _, err := detectAtakVariant(filepath.Join(dir, v, row.ApkPath), info); err == nil {
					name += " (" + variant + ")"
					if cfg != nil && variant != cfg.Atak {
						r.warnf("%s: ATAK %s is distributed, but atak: %s in %s", v, variant, cfg.Atak, configFileName)
					}
				}
			}
			summary = append(summary, name)
			if byPkg[row.Package] == nil {
				byPkg[row.Package] = map[string]productInfRow{}
				certs[row.Package] = map[string][]string{}
			}
			byPkg[row.Package][v] = row
			names[row.Package] = row.DisplayName
			if fps, err := signingCerts(filepath.Join(dir, v, row.ApkPath)); err == nil {
				certs[row.Package][v] = fps
			}
		}
		if len(summary) == 0 {
			summary = []string{"(nothing)"}
		}
		r.summary = append(r.summary, versionSummary{v, summary})
	}

	// Packages missing from some versions: the packages of taktool.yaml, or
	// otherwise every package found in any version
	var expected []string
	if cfg != nil && len(cfg.Packages) > 0 {
		expected = cfg.Packages
	} else {
		for pkg := range byPkg {
			expected = append(expected, pkg)
		}
		slices.Sort(expected)
	}
	for _, pkg := range expected {
		name := names[pkg]
		if name == "" {
			name = pkg
		}
		var in []string
		for _, v := range versions {
			if _, ok := byPkg[pkg][v]; ok {
				in = append(in, v)
			}
		}
		for _, v := range versions {
			if _, ok := byPkg[pkg][v]; ok {
				continue
			}
			if len(in) > 0 {
				r.warnf("%s: no %s (%s); it is in %s", v, name, pkg, strings.Join(in, ", "))
			} else {
				r.warnf("%s: no %s (%s)", v, name, pkg)
			}
		}
	}

	// The same package signed with different certificates in different
	// versions: devices cannot update from one to the other
	for _, pkg := range slices.Sorted(maps.Keys(certs)) {
		var first string
		for _, v := range versions {
			fps, ok := certs[pkg][v]
			if !ok {
				continue
			}
			if first == "" {
				first = v
				continue
			}
			if !slices.Equal(fps, certs[pkg][first]) {
				r.warnf("%s: the apks in %s and %s are signed with different certificates", names[pkg], first, v)
			}
		}
	}

	return r, nil
}

// checkVersionFolder checks dir/v/product.infz against the apks in dir/v and
// returns the rows of its product.inf.
func checkVersionFolder(r *checkResult, dir, v string) []productInfRow {
	vdir := filepath.Join(dir, v)
	if _, err := os.Stat(vdir); err != nil {
		r.errorf("%s: folder is missing", v)
		return nil
	}

	infzPath := filepath.Join(vdir, productInfzFilename)
	if lfs, _ := isLFSPointer(infzPath); lfs {
		r.errorf("%s/%s is a Git LFS pointer", v, productInfzFilename)
		return nil
	}
	zr, err := zip.OpenReader(infzPath)
	if err != nil {
		r.errorf("%s/%s: %v", v, productInfzFilename, err)
		return nil
	}
	defer zr.Close()

	inZip := map[string]*zip.File{}
	for _, f := range zr.File {
		inZip[f.Name] = f
	}
	inf, ok := inZip[productInfFilename]
	if !ok {
		r.errorf("%s/%s has no %s", v, productInfzFilename, productInfFilename)
		return nil
	}
	rows, err := readProductInf(inf)
	if err != nil {
		r.errorf("%s/%s: %v", v, productInfFilename, err)
		return nil
	}

	listed := map[string]bool{}
	hasAtak := false
	for _, row := range rows {
		listed[row.ApkPath] = true
		hasAtak = hasAtak || isAtakApp(row.Package)
		checkRow(r, vdir, v, row, inZip)
	}
	if !hasAtak {
		r.warnf("%s: no ATAK apk", v)
	}

	// Apks in the folder that product.inf does not list
	entries, _ := os.ReadDir(vdir)
	for _, e := range entries {
		if !e.IsDir() && isApkFile(e.Name()) && !listed[e.Name()] {
			r.warnf("%s/%s is not listed in product.inf", v, e.Name())
		}
	}
	return rows
}

func checkRow(r *checkResult, vdir, v string, row productInfRow, inZip map[string]*zip.File) {
	where := v + "/" + row.ApkPath
	path := filepath.Join(vdir, filepath.FromSlash(row.ApkPath))

	info, err := os.Stat(path)
	if err != nil {
		r.errorf("%s (%s): apk is missing", where, row.DisplayName)
		return
	}
	if lfs, _ := isLFSPointer(path); lfs {
		r.errorf("%s is a Git LFS pointer, not an apk", where)
		return
	}
	if size, err := strconv.ParseInt(row.Size, 10, 64); err != nil || size != info.Size() {
		r.errorf("%s: size %d does not match product.inf (%s)", where, info.Size(), row.Size)
	}
	if hash, err := calculateHash(path); err != nil {
		r.errorf("%s: %v", where, err)
	} else if hash != row.Hash {
		r.errorf("%s: sha256 does not match product.inf", where)
	} else if apk, err := getApkData(path); err == nil {
		// Same file: the other fields should be what taktool writes now
		if diff := productInfRowDiff(row, apk); diff != "" {
			r.warnf("%s: product.inf is out of date (%s); run 'taktool repo' to rebuild it", where, diff)
		}
	}
	if row.IconPath != "" && inZip[row.IconPath] == nil {
		r.warnf("%s: icon %s is not in product.infz", where, row.IconPath)
	}
}

// productInfRowDiff compares a product.inf row with the data read from its
// apk, and describes the first difference.
func productInfRowDiff(row productInfRow, apk ApkInfo) string {
	fields := []struct{ name, has, want string }{
		{"type", row.Type, apk.Type},
		{"package", row.Package, apk.Package},
		{"label", row.DisplayName, apk.DisplayName},
		{"version", row.Version, apk.Version},
		{"revision", row.Revision, apk.Revision},
		{"description", row.Description, apk.Description},
		{"os requirement", row.OsReq, strconv.Itoa(apk.OsReq)},
		{"tak prereq", row.TakReq, apk.TakReq},
	}
	for _, f := range fields {
		if f.has != f.want {
			return fmt.Sprintf("%s %q, the apk has %q", f.name, f.has, f.want)
		}
	}
	return ""
}

func readProductInf(f *zip.File) ([]productInfRow, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}

	var rows []productInfRow
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 13 {
			return nil, errors.New("line " + strconv.Itoa(i+1) + " does not have 13 fields")
		}
		rows = append(rows, productInfRow{
			Platform: fields[0], Type: fields[1], Package: fields[2], DisplayName: fields[3],
			Version: fields[4], Revision: fields[5], ApkPath: fields[6], IconPath: fields[7],
			Description: fields[8], Hash: fields[9], OsReq: fields[10], TakReq: fields[11], Size: fields[12],
		})
	}
	return rows, nil
}
