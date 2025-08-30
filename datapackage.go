package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
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

	var err error
	var UID uuid.UUID

	// Handle UID
	if uid == "" {
		UID = uuid.New()
	} else {
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

		// If there is / or \ in path, take name after the last one
		name = pwd[strings.LastIndex(pwd, "/")+1:]
		name = name[strings.LastIndex(name, "\\")+1:]

		// If directory name is still empty, set name as default
		if name == "" {
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

	// Get files and directories from directory
	files, err := os.ReadDir(".")
	if err != nil {
		return fmt.Errorf("error reading directory: %w", err)
	}

	// Add files to manifest with relative paths
	for _, file := range files {
		if file.IsDir() {
			// Read file path recursively
			filenames, err := readDirFiles(file.Name())
			if err != nil {
				return fmt.Errorf("error reading directory files: %w", err)
			}
			manifest.FileContents = append(manifest.FileContents, filenames...)
		} else {
			manifest.FileContents = append(manifest.FileContents, file.Name())
		}
	}

	err = makeDataPackage(manifest, dataPackageName)
	if err != nil {
		return fmt.Errorf("error making data package: %w", err)
	}

	fmt.Println("Data package created:", dataPackageName)

	return nil
}

func makeDataPackage(manifest Manifest, name string) error {
	// Create manifest
	manifestData, err := buildManifest(manifest)
	if err != nil {
		return fmt.Errorf("error building manifest: %w", err)
	}

	// Create zip file to the working directory
	file, err := os.Create(name)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}

	zipWriter := zip.NewWriter(file)
	defer zipWriter.Close()

	// Write manifest to zip
	mf, err := zipWriter.Create("MANIFEST/manifest.xml")
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	_, err = mf.Write([]byte(manifestData))
	if err != nil {
		return fmt.Errorf("error writing manifest: %w", err)
	}

	// Write files to zip
	for _, file := range manifest.FileContents {
		f, err := os.Open(file)
		if err != nil {
			return fmt.Errorf("error opening file: %w", err)
		}
		defer f.Close()

		// Create file in zip
		fw, err := zipWriter.Create(file)
		if err != nil {
			return fmt.Errorf("error creating file: %w", err)
		}

		// Copy file to zip
		_, err = io.Copy(fw, f)
		if err != nil {
			return fmt.Errorf("error copying file: %w", err)
		}
	}

	return nil
}

func readDirFiles(path string) ([]string, error) {
	files, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("error reading directory: %v", err)
	}
	var fileNames []string
	for _, file := range files {
		if file.IsDir() {
			fileNames2, err := readDirFiles(path + "/" + file.Name())
			if err != nil {
				return nil, fmt.Errorf("error reading directory files: %v", err)
			}
			fileNames = append(fileNames, fileNames2...)
			continue
		}
		fileNames = append(fileNames, path+"/"+file.Name())
	}
	return fileNames, nil
}

func buildManifest(manifest Manifest) (string, error) {

	template := template.New("manifest")

	strTemplate := `<MissionPackageManifest version="2">
  <Configuration>
    <Parameter name="uid" value="{{ .UID }}"/>
    <Parameter name="name" value="{{ .Name }}"/>
	<Parameter name="onReceiveImport" value="{{ .OnReceiveImport }}"/>
    <Parameter name="onReceiveDelete" value="{{ .OnReceiveDelete }}"/>
  </Configuration>
  <Contents>{{range .FileContents }}
        <Content ignore="false" zipEntry="{{ . }}"/>{{end}}
  </Contents>
</MissionPackageManifest>`

	template, err := template.Parse(strTemplate)
	if err != nil {
		return "", fmt.Errorf("error parsing template: %w", err)
	}

	var manifestStr bytes.Buffer
	// Execute the template
	err = template.Execute(&manifestStr, manifest)
	if err != nil {
		panic(err)
	}

	// Return the manifest as string
	return manifestStr.String(), nil
}

func removeFirstDotIfPresent(s string) string {

	if len(s) == 0 {
		return s
	}
	
	if s[0] == '.' {
		return s[1:]
	}
	return s
}

