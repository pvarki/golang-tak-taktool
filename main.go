package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:  %s\n\n", "taktool COMMAND [OPTIONS]")
		// Print commands
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  pluginspackage, pp\tCreate plugins package\n")
		fmt.Fprintf(os.Stderr, "  datapackage, dp\tCreate data package\n\n")
		// Print options
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}

	// Note: flags are not parsed as flag package does not like arguments without dashes. These flags are only for usage print
	flag.String("dbname", "", "Set data package name (default is current directory name)")
	flag.String("dbuid", "", "Set data package UID (default is randomly generated)")
	flag.String("dbext", "dpk", "Set data package file extension")
	flag.Bool("deleteonreceive", false, "Set data package \"onReceiveDelete\" to delete the package after receive")
	flag.Bool("importonreceive", false, "Set data package \"onReceiveImport\" to import the package after receive")
	flag.Bool("renamepluginsdisabled", false, "Disable renaming of plugins to preferred names. Renaming removes older plugins with the same name.")

	flag.Parse()

	dontRenamePlugins, dpDeleteOnReceive, dpImportOnReceive, dpName, dpUID, dpExt := manualFlagsParse() // Flag package cant parse flags if agruments without dash is used

	// If no arguments, print usage
	if flag.NArg() == 0 {
		flag.Usage()
		return
	}

	arg0 := flag.Arg(0)
	switch arg0 {
	case "pluginspackage", "pp":
		// Handle pluginspackage command
		err := PackagePlugins(!dontRenamePlugins)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating plugins package: %v\n", err)
			os.Exit(1)
		}
	case "datapackage", "dp":
		// Handle datapackage command
		err := PackageDataPackage(
			dpUID,
			dpName,
			dpExt,
			dpDeleteOnReceive,
			dpImportOnReceive,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating data package: %v\n", err)
			os.Exit(1)
		}
	default:
		// Print usage if unknown command
		flag.Usage()
	}
}

func manualFlagsParse() (dontRenamePlugins, dpDeleteOnReceive, dpImportOnReceive bool, dpName, dpUID, dpExt string) {

	// Datapackage default file extension
	dpExt = "dpk"

	for _, arg := range os.Args[1:] {
		switch arg {
		case "-renamepluginsdisabled":
			dontRenamePlugins = true
		case "-deleteonreceive":
			dpDeleteOnReceive = true
		case "-importonreceive":
			dpImportOnReceive = true
		default:
			if strings.HasPrefix(arg, "-dpname=") {
				dpName = strings.TrimPrefix(arg, "-dpname=")
			} else if strings.HasPrefix(arg, "-dpuid=") {
				dpUID = strings.TrimPrefix(arg, "-dpuid=")
			} else if strings.HasPrefix(arg, "-dpext=") {
				dpExt = strings.TrimPrefix(arg, "-dpext=")
			}
		}
	}

	return
}

