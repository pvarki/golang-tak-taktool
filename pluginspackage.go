package main

import (
	"archive/zip"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/avast/apkparser"
)

type ApkInfo struct {
	Platform    string
	Type        string
	Package     string
	DisplayName string
	Version     string
	Revision    string
	ApkPath     string
	IconPath    string
	Description string
	Hash        string
	OsReq       int
	TakReq      string
	Size        int

	// srcPath is where the apk file is, if it is not ApkPath in the folder
	// product.infz is written to (used to read the icon)
	srcPath string
}

const productInfzFilename = "product.infz"
const productInfFilename = "product.inf"

// 1x1 transparent png, used when the apk has no usable png icon
var emptyPng = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}

func isApkFile(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".apk")
}

func PackagePlugins(renamePlugins bool) error {
	apkInfos := []ApkInfo{}

	// Read current directory, for now...
	dirContents, err := os.ReadDir(".")
	if err != nil {
		return fmt.Errorf("error reading directory: %w", err)
	}

	// Loop through directory contents and get apk data from each apk file
	for _, entry := range dirContents {
		if entry.IsDir() || !isApkFile(entry.Name()) {
			continue
		}
		if lfs, err := isLFSPointer(entry.Name()); err != nil {
			return err
		} else if lfs {
			return fmt.Errorf("%s is a Git LFS pointer, not an apk; fetch the real file (git lfs pull)", entry.Name())
		}
		apkData, err := getApkData(entry.Name())
		if err != nil {
			return fmt.Errorf("error getting apk data from %s: %w", entry.Name(), err)
		}
		apkInfos = append(apkInfos, apkData)
	}

	// If renamePlugins is true, remove older versions of the same package and
	// rename the apk files to their preferred names
	if renamePlugins {
		// Check everything before anything is deleted or renamed
		kept, older, err := planOlderVersions(apkInfos)
		if err != nil {
			return fmt.Errorf("error removing older versions: %w", err)
		}
		if err := checkRenames(kept, older); err != nil {
			return fmt.Errorf("error renaming plugins: %w", err)
		}
		if err := removeOlder(older); err != nil {
			return fmt.Errorf("error removing older versions: %w", err)
		}
		apkInfos, err = RenamePlugins(kept)
		if err != nil {
			return fmt.Errorf("error renaming plugins: %w", err)
		}
	}

	// product.inf is comma separated, so the file names must not contain commas
	for _, a := range apkInfos {
		if strings.ContainsAny(a.ApkPath, ",\r\n") {
			return fmt.Errorf("file name %q contains a comma or line break, which product.inf cannot hold; rename the file", a.ApkPath)
		}
	}

	if err := writeProductInfz(".", apkInfos, "images", os.Stdout); err != nil {
		return err
	}

	fmt.Println("Package created:", productInfzFilename)

	return nil
}

// writeProductInfz writes dir/product.infz for the apks in dir. The ApkPath
// of each apkInfo is relative to dir. Custom icons are read from imagesDir.
// Notes about icons are written to log. The file is written to a temporary
// file and renamed only after everything has been written.
func writeProductInfz(dir string, apkInfos []ApkInfo, imagesDir string, log io.Writer) (err error) {
	name := filepath.Join(dir, productInfzFilename)
	tmpName := name + ".tmp"
	file, err := os.Create(tmpName)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	defer func() {
		if err != nil {
			file.Close()
			os.Remove(tmpName)
		}
	}()
	if err = buildProductInfz(file, dir, apkInfos, imagesDir, log); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("error closing file: %w", err)
	}
	if err = os.Rename(tmpName, name); err != nil {
		return fmt.Errorf("error renaming %s to %s: %w", tmpName, name, err)
	}
	return nil
}

// buildProductInfz writes product.infz (a zip with the icons and
// product.inf) to w. It sets the IconPath of each apkInfo.
func buildProductInfz(w io.Writer, dir string, apkInfos []ApkInfo, imagesDir string, log io.Writer) error {
	if log == nil {
		log = io.Discard
	}
	customImagesList, err := listCustomImages(imagesDir)
	if err != nil {
		return fmt.Errorf("error checking for custom images: %w", err)
	}
	zipWriter := zip.NewWriter(w)

	// Get icon files from apk files and add them to zip
	for i := range apkInfos {
		if err := addIcon(zipWriter, dir, &apkInfos[i], imagesDir, customImagesList, log); err != nil {
			return err
		}
	}

	// Write product.inf
	mf, err := zipWriter.Create(productInfFilename)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	if _, err = mf.Write([]byte(createProductInf(apkInfos))); err != nil {
		return fmt.Errorf("error writing product.inf: %w", err)
	}

	// Close writes the zip central directory, so its error must be checked
	if err = zipWriter.Close(); err != nil {
		return fmt.Errorf("error finishing zip: %w", err)
	}
	return nil
}

// addIcon writes the icon of the apk into the zip as <apk name>.png and sets
// apkInfo.IconPath to that name. A custom image from imagesDir is used if
// there is one; otherwise the icon of the apk (converted to png if needed);
// otherwise an empty png.
func addIcon(zw *zip.Writer, dir string, apkInfo *ApkInfo, imagesDir string, customImagesList []string, log io.Writer) error {
	iconInApk := apkInfo.IconPath
	newImageFileName := strings.TrimSuffix(apkInfo.ApkPath, filepath.Ext(apkInfo.ApkPath)) + ".png"
	apkInfo.IconPath = newImageFileName

	fw, err := zw.Create(newImageFileName)
	if err != nil {
		return fmt.Errorf("error creating icon file into zip: %w", err)
	}

	// If a custom image is found, use it instead of the image from the apk
	// package. It is named like the apk (vns_plugin.png) or after the
	// package name (com.atakmap.android.vns.plugin.png).
	if custom := customImageFor(*apkInfo, newImageFileName, customImagesList); custom != "" {
		if err := copyFile(fw, filepath.Join(imagesDir, custom)); err != nil {
			return fmt.Errorf("error copying custom image file: %w", err)
		}
		fmt.Fprintln(log, "Using custom image for package", apkInfo.DisplayName, ":", custom)
		return nil
	}

	src := apkInfo.srcPath
	if src == "" {
		src = filepath.Join(dir, apkInfo.ApkPath)
	}
	data, kind, _, err := cachedApkIcon(src, iconInApk)
	if err != nil {
		fmt.Fprintf(log, "No usable icon for %s (%v), using an empty icon\n", apkInfo.DisplayName, err)
		data = emptyPng
	} else if kind != "png" {
		fmt.Fprintf(log, "Icon of %s converted to png from %s\n", apkInfo.DisplayName, kind)
	}
	_, err = fw.Write(data)
	return err
}

func copyFile(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// copyFileFromZip copies the file name from the zip archive at zipPath to w.
// It returns false if the archive has no such file.
func copyFileFromZip(w io.Writer, zipPath, name string) (bool, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return false, err
	}
	defer zr.Close()

	for _, zf := range zr.File {
		if zf.Name != name {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return false, err
		}
		defer rc.Close()
		_, err = io.Copy(w, rc)
		return true, err
	}
	return false, nil
}

// customImageFor returns the custom image for an apk in the images folder,
// or "". It can be named, in this order of preference:
//   - like the apk in the update folder: vns_plugin.png
//   - after the package: com.atakmap.android.vns.plugin.png
//   - after the app's short name, the label without spaces as in the manual
//     names: datasync.png for "Data Sync" (case, '-' and '_' are ignored)
func customImageFor(a ApkInfo, apkImageName string, customImagesList []string) string {
	for _, name := range []string{apkImageName, a.Package + ".png"} {
		for _, c := range customImagesList {
			if strings.EqualFold(c, name) {
				return c
			}
		}
	}
	if short := normalizeImageName(shortImageName(a)); short != "" {
		for _, c := range customImagesList {
			if normalizeImageName(c) == short {
				return c
			}
		}
	}
	return ""
}

// shortImageName returns the short custom image name of an app,
// e.g. "datasync.png" for "Data Sync".
func shortImageName(a ApkInfo) string {
	if a.DisplayName == "" {
		return ""
	}
	return strings.ToLower(compactName(a.DisplayName)) + ".png"
}

// normalizeImageName lowercases a file name and removes everything but
// letters and digits before the extension: "MAVSDK-Anafi.PNG" -> "mavsdkanafi".
func normalizeImageName(name string) string {
	name = strings.TrimSuffix(strings.ToLower(name), ".png")
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, name)
}

// listCustomImages lists the png files in imagesDir (if it exists).
func listCustomImages(imagesDir string) ([]string, error) {
	customImagesList := []string{}

	// Check if the images directory exists
	if _, err := os.Stat(imagesDir); os.IsNotExist(err) {
		return nil, nil
	}

	dirContents, err := os.ReadDir(imagesDir)
	if err != nil {
		return nil, fmt.Errorf("error reading directory: %w", err)
	}

	// Add all png files in the images directory to the customImagesList
	for _, entry := range dirContents {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".png") {
			customImagesList = append(customImagesList, entry.Name())
		}
	}

	return customImagesList, nil
}

// olderApk is an apk that is replaced by a newer revision (or is a copy).
type olderApk struct {
	apk, keep ApkInfo
}

// planOlderVersions decides which apks are older revisions or copies of
// another apk of the same package, without changing anything.
func planOlderVersions(apkInfos []ApkInfo) (kept []ApkInfo, older []olderApk, err error) {
	newest := map[string]int{}  // package name -> index of the newest apk so far
	replacedBy := map[int]int{} // index of an older apk -> index of the apk that replaces it

	for i, a := range apkInfos {
		j, seen := newest[a.Package]
		if !seen {
			newest[a.Package] = i
			continue
		}
		b := apkInfos[j]

		revA, errA := strconv.ParseInt(a.Revision, 10, 64)
		revB, errB := strconv.ParseInt(b.Revision, 10, 64)
		if errA != nil || errB != nil {
			return nil, nil, fmt.Errorf("cannot compare %s and %s (package %s): revision code %q / %q is not a number",
				a.ApkPath, b.ApkPath, a.Package, a.Revision, b.Revision)
		}

		switch {
		case revA > revB:
			replacedBy[j] = i
			newest[a.Package] = i
		case revA < revB:
			replacedBy[i] = j
		case a.Hash == b.Hash:
			replacedBy[i] = j // identical copy
		default:
			return nil, nil, fmt.Errorf("%s and %s (package %s) have the same revision code %s but different content; remove one of them",
				a.ApkPath, b.ApkPath, a.Package, a.Revision)
		}
	}

	// The newest apk of each package
	keep := func(i int) ApkInfo {
		for {
			next, ok := replacedBy[i]
			if !ok {
				return apkInfos[i]
			}
			i = next
		}
	}
	for i, a := range apkInfos {
		if _, isOlder := replacedBy[i]; isOlder {
			older = append(older, olderApk{apk: a, keep: keep(i)})
		} else {
			kept = append(kept, a)
		}
	}
	return kept, older, nil
}

// removeOlder deletes the older apks.
func removeOlder(older []olderApk) error {
	for _, o := range older {
		fmt.Printf("Removing older version: %s revision %s (%s), keeping revision %s (%s)\n",
			o.apk.DisplayName, o.apk.Revision, o.apk.ApkPath, o.keep.Revision, o.keep.ApkPath)
		if signersDiffer(o.apk.ApkPath, o.keep.ApkPath) {
			fmt.Printf("Warning: %s and %s are signed with different certificates; devices cannot update from one to the other, the old version must be uninstalled first\n",
				o.apk.ApkPath, o.keep.ApkPath)
		}
		if err := os.Remove(o.apk.ApkPath); err != nil {
			return err
		}
	}
	return nil
}

// checkRenames checks that the apks can be renamed to their preferred names:
// no two get the same name, and no other file has the name (files that are
// going to be removed do not count).
func checkRenames(apkInfos []ApkInfo, removed []olderApk) error {
	gone := map[string]bool{}
	for _, o := range removed {
		gone[strings.ToLower(o.apk.ApkPath)] = true
	}
	targets := map[string]ApkInfo{}
	for _, a := range apkInfos {
		newName := preferredApkName(a)
		key := strings.ToLower(newName)
		if other, dup := targets[key]; dup {
			return fmt.Errorf("%s (%s) and %s (%s) would both be renamed to %s; use -renamepluginsdisabled or keep them in different directories",
				a.ApkPath, a.Package, other.ApkPath, other.Package, newName)
		}
		targets[key] = a
		if newName == a.ApkPath || gone[key] {
			continue
		}
		if dst, err := os.Stat(newName); err == nil {
			src, err := os.Stat(a.ApkPath)
			if err != nil {
				return err
			}
			if !os.SameFile(src, dst) {
				return fmt.Errorf("cannot rename %s to %s: %s already exists", a.ApkPath, newName, newName)
			}
		}
	}
	return nil
}

// preferredApkName returns the file name an apk is renamed to,
// e.g. "atak_app.apk" or "vns_plugin.apk".
func preferredApkName(a ApkInfo) string {
	name := a.DisplayName
	if name == "" {
		name = a.Package
	}
	return reworkPluginName(name+"_"+a.Type) + ".apk"
}

// RenamePlugins renames the apk files to their preferred names. It checks all
// new names before renaming anything and never overwrites another file.
func RenamePlugins(apkInfos []ApkInfo) ([]ApkInfo, error) {
	if err := checkRenames(apkInfos, nil); err != nil {
		return apkInfos, err
	}
	for i, a := range apkInfos {
		newName := preferredApkName(a)
		if newName == a.ApkPath {
			continue
		}
		if err := os.Rename(a.ApkPath, newName); err != nil {
			return apkInfos, err
		}
		apkInfos[i].ApkPath = newName
	}
	return apkInfos, nil
}

// readApkData reads the data of an apk for product.inf from the file; use
// getApkData, which caches it.
func readApkData(apkPath string) (ApkInfo, error) {
	var err error

	//Buffer to write the xml data into
	var bufBytes []byte
	buffer := bytes.NewBuffer(bufBytes)

	enc := xml.NewEncoder(buffer)
	enc.Indent("", "\t")

	zipErr, resErr, manErr := apkparser.ParseApk(apkPath, enc)
	if zipErr != nil {
		return ApkInfo{}, fmt.Errorf("failed to open the APK: %w", zipErr)
	}
	if resErr != nil {
		return ApkInfo{}, fmt.Errorf("failed to parse resources: %w", resErr)
	}
	if manErr != nil {
		return ApkInfo{}, fmt.Errorf("failed to parse AndroidManifest.xml: %w", manErr)
	}

	apkData, err := readParametersFromXML(buffer)
	if err != nil {
		return ApkInfo{}, fmt.Errorf("error reading parameters from XML: %w", err)
	}
	if apkData.Package == "" {
		return ApkInfo{}, fmt.Errorf("package name not found in AndroidManifest.xml")
	}

	apkData.ApkPath = apkPath

	// Calculate size of apk file
	info, err := os.Stat(apkPath)
	if err != nil {
		return ApkInfo{}, fmt.Errorf("error getting file info: %w", err)
	}
	apkData.Size = int(info.Size())

	// Calculate hash SHA-256 for apk file
	apkData.Hash, err = verifiedHash(apkPath)
	if err != nil {
		return ApkInfo{}, fmt.Errorf("error calculating hash: %w", err)
	}

	return apkData, nil
}

func readParametersFromXML(reader io.Reader) (ApkInfo, error) {
	decoder := xml.NewDecoder(reader)
	apkData := ApkInfo{
		Platform: "Android",
		OsReq:    1,
	}

	for {
		t, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ApkInfo{}, fmt.Errorf("error reading XML: %w", err)
		}

		se, ok := t.(xml.StartElement)
		if !ok {
			continue
		}

		switch se.Name.Local {
		case "manifest":
			if v, ok := attr(se, "package"); ok {
				apkData.Package = cleanupValue(v)
			}
			if v, ok := attr(se, "versionCode"); ok {
				apkData.Revision = cleanupValue(v)
			}
			if v, ok := attr(se, "versionName"); ok {
				apkData.Version = cleanupValue(v)
			}
		case "uses-sdk":
			// "os requirement" is the minimum Android API level, as in the
			// product.inf that TAK builds for the apps bundled with ATAK.
			// ATAK shows it in the app details. Keep the default 1 (what
			// ATAK itself writes when it does not know) if it is missing.
			if v, ok := attr(se, "minSdkVersion"); ok {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
					apkData.OsReq = n
				}
			}
		case "application":
			if v, ok := attr(se, "label"); ok {
				apkData.DisplayName = cleanupValue(v)
			}
			if v, ok := attr(se, "description"); ok {
				apkData.Description = cleanupValue(v)
			}
			if v, ok := attr(se, "icon"); ok {
				apkData.IconPath = cleanupValue(v)
			}
		case "meta-data":
			name, _ := attr(se, "name")
			value, hasValue := attr(se, "value")
			if !hasValue {
				continue
			}
			if name == "plugin-api" {
				apkData.TakReq = cleanupValue(value)
			}
			if name == "app_desc" && apkData.Description == "" {
				apkData.Description = cleanupValue(value)
			}
		}
	}

	// As in ATAK (ProductInformation.create): an apk is a plugin if it has
	// the plugin-api meta-data and is not ATAK itself, which has it too.
	// The package name does not matter (Beartooth is
	// com.beartooth.beartoothtakplugin).
	apkData.Type = "app"
	if apkData.TakReq != "" && !isAtakApp(apkData.Package) {
		apkData.Type = "plugin"
	}

	return apkData, nil
}

// Calculate hash SHA-256 from file
func calculateHash(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("error opening file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("error copying file: %w", err)
	}

	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// Create product.inf file content
func createProductInf(apkInfos []ApkInfo) string {
	// First line of product.inf file
	productInf := "#platform (Android Windows or iOS), type (app or plugin), full package name, display/label, version, revision code (integer), relative path to APK file, relative path to icon file, description, apk hash, os requirement, tak prereq (e.g. plugin-api), apk size"

	// Order apkInfos
	apkInfos = sortApkInfos(apkInfos)

	// Loop through apkInfos and add them to productInf
	for _, apkInfo := range apkInfos {
		productInf += fmt.Sprintf("\n%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%d,%s,%d",
			apkInfo.Platform,
			apkInfo.Type,
			apkInfo.Package,
			apkInfo.DisplayName,
			apkInfo.Version,
			apkInfo.Revision,
			apkInfo.ApkPath,
			apkInfo.IconPath,
			apkInfo.Description,
			apkInfo.Hash,
			apkInfo.OsReq,
			apkInfo.TakReq,
			apkInfo.Size,
		)
	}

	return productInf
}

// Clean up value by cutting it at line break and removing commas
func cleanupValue(str string) string {
	// Cut string at line break
	str = strings.Split(str, "\n")[0]
	// Remove commas from string
	str = strings.ReplaceAll(str, ",", "")
	return str
}

// Order apkInfos by Platform, Type, Package, DisplayName, Version
func sortApkInfos(apkInfos []ApkInfo) []ApkInfo {

	slices.SortFunc(apkInfos, func(a, b ApkInfo) int {
		return cmp.Or(
			cmp.Compare(a.Platform, b.Platform),
			cmp.Compare(a.Type, b.Type),
			cmp.Compare(a.Package, b.Package),
			cmp.Compare(a.DisplayName, b.DisplayName),
			cmp.Compare(a.Version, b.Version),
		)
	})

	return apkInfos
}

// reworkPluginName makes a file name from a plugin name: lowercase, with
// spaces, dots and any other characters that are not letters, digits, '-' or
// '_' replaced with underscores.
func reworkPluginName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return unicode.ToLower(r)
		}
		return '_'
	}, name)

	// If the name end with _plugin_plugin or _app_app, remove the duplicate suffix
	name = strings.ReplaceAll(name, "_plugin_plugin", "_plugin")
	name = strings.ReplaceAll(name, "_app_app", "_app")
	return name
}
