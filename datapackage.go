package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/google/uuid"
)

type Manifest struct {
	UID             string
	Name            string
	FileContents    []string
	OnReceiveDelete bool
	OnReceiveImport bool
}

func PackageDataPackage(uid, name, fileExtension string, onReceiveDelete, onReceiveImport bool) error {
	// Handle UID
	var UID uuid.UUID
	if uid == "" {
		UID = uuid.New()
	} else {
		var err error
		UID, err = uuid.Parse(uid)
		if err != nil {
			return fmt.Errorf("error parsing UID: %w", err)
		}
	}

	// Handle name
	if name == "" {
		// Get current directory name, if name is not set by user
		pwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("error getting working directory: %w", err)
		}
		name = filepath.Base(pwd)

		// filepath.Base returns "/" or "." for a root or empty path
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = "default"
		}
	}
	dataPackageName := name + "." + removeFirstDotIfPresent(fileExtension)

	// Create manifest
	manifest := Manifest{
		UID:             UID.String(),
		Name:            name,
		OnReceiveDelete: onReceiveDelete,
		OnReceiveImport: onReceiveImport,
	}

	// Collect files recursively. The package being created (and its temporary
	// file) is skipped, so that a previous run's package is not put inside the
	// new one.
	files, err := collectFiles(".", "."+removeFirstDotIfPresent(fileExtension), dataPackageName, dataPackageName+".tmp")
	if err != nil {
		return fmt.Errorf("error reading directory: %w", err)
	}
	manifest.FileContents = files

	err = makeDataPackage(manifest, dataPackageName)
	if err != nil {
		return fmt.Errorf("error making data package: %w", err)
	}

	fmt.Println("Data package created:", dataPackageName)

	return nil
}

// collectFiles returns the regular files under root as slash separated paths
// relative to root, sorted. Left out are: files in root named in exclude,
// data packages in root with the extension packageExt (packages of earlier
// runs; other zips are packed),
// hidden files and folders (.git, .DS_Store) and Windows' Thumbs.db and
// desktop.ini.
func collectFiles(root, packageExt string, exclude ...string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel != "." && (strings.HasPrefix(d.Name(), ".") ||
			strings.EqualFold(d.Name(), "Thumbs.db") || strings.EqualFold(d.Name(), "desktop.ini")) {
			fmt.Println("Skipping (hidden or system file):", filepath.ToSlash(rel))
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if slices.Contains(exclude, rel) {
			return nil
		}
		if packageExt != "" && !strings.ContainsRune(rel, filepath.Separator) && strings.EqualFold(filepath.Ext(rel), packageExt) &&
			isDataPackage(path) {
			fmt.Println("Skipping (an earlier data package):", rel)
			return nil
		}

		// Follow symlinks to files, skip anything that is not a regular file
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			fmt.Println("Skipping (not a regular file):", rel)
			return nil
		}

		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}

// makeDataPackage writes the package to a temporary file first and renames it
// to name only after everything has been written, so that a failed run does
// not leave a broken package behind.
func makeDataPackage(manifest Manifest, name string) (err error) {
	manifestData, err := buildManifest(manifest)
	if err != nil {
		return fmt.Errorf("error building manifest: %w", err)
	}

	tmpName := name + ".tmp" // keep in sync with the exclude list in PackageDataPackage
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

	zipWriter := zip.NewWriter(file)

	// Write manifest to zip
	mf, err := zipWriter.Create("MANIFEST/manifest.xml")
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	if _, err = mf.Write([]byte(manifestData)); err != nil {
		return fmt.Errorf("error writing manifest: %w", err)
	}

	// Write files to zip
	for _, f := range manifest.FileContents {
		if err = addFileToZip(zipWriter, f, f); err != nil {
			return err
		}
	}

	// Close writes the zip central directory, so its error must be checked
	if err = zipWriter.Close(); err != nil {
		return fmt.Errorf("error finishing zip: %w", err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("error closing file: %w", err)
	}
	if err = os.Rename(tmpName, name); err != nil {
		return fmt.Errorf("error renaming %s to %s: %w", tmpName, name, err)
	}
	return nil
}

// addFileToZip copies the file at srcPath into the zip as zipName.
func addFileToZip(zw *zip.Writer, srcPath, zipName string) error {
	f, err := os.Open(filepath.FromSlash(srcPath))
	if err != nil {
		return fmt.Errorf("error opening file: %w", err)
	}
	defer f.Close()

	fw, err := zw.Create(zipName)
	if err != nil {
		return fmt.Errorf("error creating file %s in zip: %w", zipName, err)
	}
	if _, err := io.Copy(fw, f); err != nil {
		return fmt.Errorf("error copying file %s: %w", srcPath, err)
	}
	return nil
}

const manifestTemplate = `<MissionPackageManifest version="2">
  <Configuration>
    <Parameter name="uid" value="{{ xml .UID }}"/>
    <Parameter name="name" value="{{ xml .Name }}"/>
    <Parameter name="onReceiveImport" value="{{ .OnReceiveImport }}"/>
    <Parameter name="onReceiveDelete" value="{{ .OnReceiveDelete }}"/>
  </Configuration>
  <Contents>{{range .FileContents }}
        <Content ignore="false" zipEntry="{{ xml . }}"/>{{end}}
  </Contents>
</MissionPackageManifest>`

// xmlEscape escapes a string for use in XML text and attribute values.
func xmlEscape(s string) (string, error) {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return "", err
	}
	return b.String(), nil
}

func buildManifest(manifest Manifest) (string, error) {
	tmpl, err := template.New("manifest").
		Funcs(template.FuncMap{"xml": xmlEscape}).
		Parse(manifestTemplate)
	if err != nil {
		return "", fmt.Errorf("error parsing template: %w", err)
	}

	var manifestStr bytes.Buffer
	if err := tmpl.Execute(&manifestStr, manifest); err != nil {
		return "", fmt.Errorf("error executing template: %w", err)
	}

	return manifestStr.String(), nil
}

func removeFirstDotIfPresent(s string) string {
	return strings.TrimPrefix(s, ".")
}

// isDataPackage reports whether the file is a zip with MANIFEST/manifest.xml,
// i.e. a TAK data package.
func isDataPackage(path string) bool {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	defer zr.Close()
	for _, f := range zr.File {
		if strings.EqualFold(f.Name, "MANIFEST/manifest.xml") {
			return true
		}
	}
	return false
}
