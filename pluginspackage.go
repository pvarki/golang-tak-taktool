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
}

const proructInfzFilename = "product.infz"
const productInfFilename = "product.inf"

func PackagePlugins(renamePlugins bool) error {
	apkInfos := []ApkInfo{}

	// Read current directory, for now...
	dirContents, err := os.ReadDir(".")
	if err != nil {
		return fmt.Errorf("error reading directory: %w", err)
	}

	// Loop through directory contents and get apk data from each apk file
	for _, entry := range dirContents {
		// Check that it is a file and that it is an apk file
		if !entry.IsDir() && strings.Contains(entry.Name(), ".apk") {

			apkData, err := getApkData(entry.Name())
			if err != nil {
				return fmt.Errorf("error getting apk data: %w", err)
			}

			apkInfos = append(apkInfos, apkData)
		}
	}

	// If renamePlugins is true, rework the name of the apk file and remove older versions of the same name plugin
	if renamePlugins {
		apkInfos, err = RemoveOlderPluginVersions(apkInfos)
		if err != nil {
			return fmt.Errorf("error removing older versions: %w", err)
		}
		apkInfos, err = RenamePlugins(apkInfos)
		if err != nil {
			return fmt.Errorf("error renaming plugins: %w", err)
		}
	}

	// Create product.infz zip
	file, err := os.Create(proructInfzFilename)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	zipWriter := zip.NewWriter(file)
	defer zipWriter.Close()

	// Check if there are custom images in the images directory
	customImagesList, err := checkForCustomImages()
	if err != nil {
		return fmt.Errorf("error checking for custom images: %w", err)
	}

	// Get icon files from apk files and add them to zip
	for i, apkInfo := range apkInfos {
		f, err := os.Open(apkInfo.ApkPath)
		if err != nil {
			return fmt.Errorf("error opening file: %w", err)
		}
		defer f.Close()

		zipReader, err := zip.NewReader(f, int64(apkInfo.Size))
		if err != nil {
			return fmt.Errorf("error reading zip: %w", err)
		}

		newImageFileName := strings.TrimSuffix(apkInfo.ApkPath, ".apk") + ".png"
		apkInfos[i].IconPath = newImageFileName

		customImageFound := slices.Contains(customImagesList, newImageFileName)

		fw, err := zipWriter.Create(newImageFileName)
		if err != nil {
			return fmt.Errorf("error creating icon file into zip: %w", err)
		}

		// If a custom image is found, use it instead of the image from the apk package
		if customImageFound {
			// Copy custom image to zip with the name newImageFileName
			customImagePath := filepath.Join("images", newImageFileName)
			customImageFile, err := os.Open(customImagePath)
			if err != nil {
				return fmt.Errorf("error opening custom image file: %w", err)
			}
			defer customImageFile.Close()

			// Copy custom image file to zip
			_, err = io.Copy(fw, customImageFile)
			if err != nil {
				return fmt.Errorf("error copying custom image file: %w", err)
			}
			fmt.Println("Using custom image for package", apkInfo.DisplayName, ":", newImageFileName)
		} else if !strings.Contains(apkInfo.IconPath, ".png") { // check if icon file extension is not png

			fmt.Println("Package", apkInfo.DisplayName, "does not have a png icon file. Creating empty png file...")

			// TODO Might be possible to convert android xml icon to png with some library

			// Empty png file
			png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}

			// Write empty png to file
			_, err = fw.Write(png)
			if err != nil {
				return fmt.Errorf("error writing file: %w", err)
			}
		} else {
			// Look for png icon file in apk
			for _, zipFile := range zipReader.File {
				if zipFile.Name == apkInfo.IconPath {
					imageFile, err := zipFile.Open()
					if err != nil {
						return fmt.Errorf("error opening file: %w", err)
					}
					defer imageFile.Close()

					// TODO
					// Check image size and scale it down if needed to 30% of the original size
					// Repeat until the image size is e.g. under 50kb

					// Add file to zip
					_, err = io.Copy(fw, imageFile)
					if err != nil {
						return fmt.Errorf("error copying file: %w", err)
					}
					break
				}
			}
		}
	}

	// Write product.inf
	mf, err := zipWriter.Create(productInfFilename)
	if err != nil {
		return fmt.Errorf("error creating file: %w", err)
	}
	_, err = mf.Write([]byte(createProductInf(apkInfos)))
	if err != nil {
		return fmt.Errorf("error writing product.inf: %w", err)
	}

	fmt.Println("Package created:", proructInfzFilename)

	return nil
}

// Check if there are custom images in the images directory
func checkForCustomImages() ([]string, error) {
	customImagesList := []string{}

	// Check if the images directory exists
	if _, err := os.Stat("./images"); os.IsNotExist(err) {
		return nil, nil
	}

	// Check if there are images directory in the current directory
	dirContents, err := os.ReadDir("./images")
	if err != nil {
		return nil, fmt.Errorf("error reading directory: %w", err)
	}

	// Add all png files in the images directory to the customImagesList
	for _, entry := range dirContents {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".png") {
			customImagesList = append(customImagesList, entry.Name())
		}
	}

	return customImagesList, nil
}

func RemoveOlderPluginVersions(apkInfos []ApkInfo) ([]ApkInfo, error) {
	// Loop through apkInfos and check if there are duplicate names (DisplayName + "_" + Type)
	// If there are duplicates, remove the older version based on the revision number
	for i := 0; i < len(apkInfos); i++ {
		for j := i + 1; j < len(apkInfos); j++ {
			// Compare DisplayName and Type
			if apkInfos[i].DisplayName == apkInfos[j].DisplayName && apkInfos[i].Type == apkInfos[j].Type {
				// Compare revision numbers as integers
				revisionI, _ := strconv.Atoi(apkInfos[i].Revision)
				revisionJ, _ := strconv.Atoi(apkInfos[j].Revision)

				// Remove the older version based on the revision number
				if revisionI < revisionJ {
					// Remove the older version
					fmt.Println("Removing older version:", apkInfos[i].DisplayName, "revision:", apkInfos[i].Revision)
					err := os.Remove(apkInfos[i].ApkPath)
					if err != nil {
						return apkInfos, err
					}
					apkInfos = append(apkInfos[:i], apkInfos[i+1:]...)
					i-- // Adjust index after removal
				} else {
					// Remove the older version
					fmt.Println("Removing older version:", apkInfos[j].DisplayName, "revision:", apkInfos[j].Revision)
					err := os.Remove(apkInfos[j].ApkPath)
					if err != nil {
						return apkInfos, err
					}
					apkInfos = append(apkInfos[:j], apkInfos[j+1:]...)
					j-- // Adjust index after removal
				}
			}
		}
	}

	return apkInfos, nil
}

func RenamePlugins(apkInfos []ApkInfo) ([]ApkInfo, error) {
	// Rename the apk files to a new name based on DisplayName and Type
	for i, apkData := range apkInfos {
		// Rename the apk file
		entryName := apkData.ApkPath
		newName := reworkPluginName(apkData.DisplayName+"_"+apkData.Type) + ".apk"

		if newName != entryName {
			// Check if the new name already exists
			if _, err := os.Stat(newName); err == nil {
				// Remove the existing file, should not happen, but just in case
				err := os.Remove(newName)
				if err != nil {
					return apkInfos, err
				}
			}

			err := os.Rename(entryName, newName)
			if err != nil {
				return apkInfos, err
			}
			entryName = newName // Update entry to the new name
		}

		filePath := filepath.Dir(apkData.ApkPath)
		if filePath == "." {
			filePath = ""
		}
		apkInfos[i].ApkPath = filePath + entryName
	}
	return apkInfos, nil
}

func getApkData(apkPath string) (ApkInfo, error) {
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

	//fmt.Println("Contents of:", apkPath)
	//fmt.Println(buffer.String())

	apkData, err := readParametersFromXML(buffer)
	if err != nil {
		return ApkInfo{}, fmt.Errorf("error reading parameters from XML: %w", err)
	}

	// Remove commas from apk path
	apkPath = cleanupValue(apkPath)

	// Add apk path and icon path to apkData
	apkData.ApkPath = apkPath
	// Calculate size of apk file
	info, err := os.Stat(apkPath)
	if err != nil {
		return ApkInfo{}, fmt.Errorf("error getting file info: %w", err)
	}
	apkData.Size = int(info.Size())

	// Calculate hash SHA-256 for apk file
	apkData.Hash, err = calculateHash(apkPath)
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

		switch se := t.(type) {
		case xml.StartElement:
			if se.Name.Local == "manifest" {
				for _, attr := range se.Attr {
					attrValue := cleanupValue(attr.Value)
					if attr.Name.Local == "package" {
						apkData.Package = attrValue
						// If the package name ends with .plugin, it is a plugin
						if apkData.Package[len(apkData.Package)-7:] == ".plugin" {
							apkData.Type = "plugin"
						} else {
							apkData.Type = "app"
						}
					} else if attr.Name.Local == "versionCode" {
						apkData.Revision = attrValue
					} else if attr.Name.Local == "versionName" {
						apkData.Version = attrValue
					}
				}
			} else if se.Name.Local == "application" {
				for _, attr := range se.Attr {
					attrValue := cleanupValue(attr.Value)
					if attr.Name.Local == "label" {
						apkData.DisplayName = attrValue
					} else if attr.Name.Local == "description" {
						apkData.Description = attrValue
					} else if attr.Name.Local == "icon" {
						apkData.IconPath = attrValue
					}
				}
			} else if se.Name.Local == "meta-data" {
				for _, attr := range se.Attr {
					if attr.Name.Local == "name" && attr.Value == "plugin-api" {
						for _, attr := range se.Attr {
							if attr.Name.Local == "value" {
								apkData.TakReq = cleanupValue(attr.Value)
								continue
							}
						}
					}
					if apkData.Description == "" && attr.Name.Local == "name" && attr.Value == "app_desc" {
						for _, attr := range se.Attr {
							if attr.Name.Local == "value" {
								apkData.Description = cleanupValue(attr.Value)
								continue
							}
						}
					}
				}
			}
		}
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

func reworkPluginName(name string) string {
	// Rework the name to be lowercase, replace spaces with underscores and dots with underscores
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, ".", "_")

	// If the name end with _plugin_plugin or _app_app, remove the duplicate suffix
	name = strings.ReplaceAll(name, "_plugin_plugin", "_plugin")
	name = strings.ReplaceAll(name, "_app_app", "_app")
	return name
}
