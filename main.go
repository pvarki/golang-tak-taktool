package main

import (
	"flag"
	"fmt"
	"os"
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

	flag.String("dpname", "", "Set data package name (default is current directory name)")
	flag.String("dpuid", "", "Set data package UID (default is randomly generated)")
	flag.String("dpext", "dpk", "Set data package file extension")
	dpdeleteonreceive := flag.Bool("dpdeleteonreceive", false, "Set data package \"onReceiveDelete\" option (default false)")
	dpimportonreceive := flag.Bool("dpimportonreceive", true, "Set data package \"onReceiveImport\" option")
	renamePlugins := flag.Bool("renameplugins", true, "Rename plugins to preferred names (default true) Note: this removes older plugins with the same name")

	flag.Parse()

	// If no arguments, print usage
	if flag.NArg() == 0 {
		flag.Usage()
		return
	}

	arg0 := flag.Arg(0)
	switch arg0 {
	case "pluginspackage", "pp":
		// Käsittele pluginspackage komento
		err := PackagePlugins(*renamePlugins)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating plugins package: %v\n", err)
			os.Exit(1)
		}
	case "datapackage", "dp":
		// Käsittele datapackage komento
		err := PackageDataPackage(
			flag.Lookup("dpuid").Value.String(),
			flag.Lookup("dpname").Value.String(),
			flag.Lookup("dpext").Value.String(),
			*dpdeleteonreceive,
			*dpimportonreceive,
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
