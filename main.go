package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage:  taktool COMMAND [OPTIONS]

Update folder for TAK Server (several ATAK versions, uses taktool.yaml):
  init                 Create the folders and taktool.yaml
  repo                 Build the update folder from the apks
  check                Check an update folder and show what it distributes
  manuals              Copy the manuals of the distributed apks

Single folder:
  pluginspackage, pp   Create product.infz for the apks in this folder
  datapackage, dp      Create a data package of this folder

Run 'taktool COMMAND -h' to see the options of a command,
and 'taktool version' to see the version.
`)
}

func main() {
	code := run(os.Args[1:], os.Stdout, os.Stderr)
	saveApkCache()
	os.Exit(code)
}

func run(args []string, stdout, stderr io.Writer) int {
	cmd, cmdArgs := splitCommand(args)
	if cmd == "" {
		usage(stderr)
		return 2
	}

	switch cmd {
	case "pluginspackage", "pp":
		opts, err := parsePluginsFlags(cmdArgs, stderr)
		if err != nil {
			return flagErrorCode(err)
		}
		if err := PackagePlugins(opts.renamePlugins); err != nil {
			fmt.Fprintf(stderr, "Error creating plugins package: %v\n", err)
			return 1
		}
	case "datapackage", "dp":
		opts, err := parseDataPackageFlags(cmdArgs, stderr)
		if err != nil {
			return flagErrorCode(err)
		}
		err = PackageDataPackage(opts.uid, opts.name, opts.ext, opts.deleteOnReceive, opts.importOnReceive)
		if err != nil {
			fmt.Fprintf(stderr, "Error creating data package: %v\n", err)
			return 1
		}
	case "repo":
		opts, err := parseRepoFlags(cmdArgs, stderr)
		if err != nil {
			return flagErrorCode(err)
		}
		if err := PackageRepository(opts, os.Stdin, stdout); err != nil {
			if errors.Is(err, errNotConfirmed) {
				fmt.Fprintln(stderr, err)
				return 1
			}
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
	case "check":
		opts, err := parseCheckFlags(cmdArgs, stderr)
		if err != nil {
			return flagErrorCode(err)
		}
		return runCheck(opts, stdout, stderr)
	case "manuals":
		opts, err := parseManualsFlags(cmdArgs, stderr)
		if err != nil {
			return flagErrorCode(err)
		}
		if err := CopyManuals(opts, stdout); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
	case "init":
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() {
			fmt.Fprintln(stderr, "Usage: taktool init [folder]")
			fmt.Fprintln(stderr, "\nCreates apks/ (with taktool.yaml and images/), update/ and manuals/ in folder")
			fmt.Fprintln(stderr, "(default: the current folder). Nothing that exists already is changed.")
		}
		if err := fs.Parse(cmdArgs); err != nil {
			return flagErrorCode(err)
		}
		base := "."
		if fs.NArg() > 1 {
			fs.Usage()
			return 2
		} else if fs.NArg() == 1 {
			base = fs.Arg(0)
		}
		if err := InitFolder(stdout, base); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, versionString())
	case "-h", "-help", "--help", "help":
		usage(stdout)
	default:
		fmt.Fprintf(stderr, "Unknown command: %s\n\n", cmd)
		usage(stderr)
		return 2
	}
	return 0
}

// splitCommand returns the first argument that is not a flag as the command,
// and all other arguments as the command's flags. This keeps the old
// behaviour where flags could be given before the command
// (e.g. "taktool -renamepluginsdisabled pp"). Flags given before the command
// must use the -flag or -flag=value form.
func splitCommand(args []string) (string, []string) {
	for i, a := range args {
		if len(a) > 0 && a[0] != '-' {
			rest := append(append([]string{}, args[:i]...), args[i+1:]...)
			return a, rest
		}
		if a == "-h" || a == "-help" || a == "--help" || a == "-version" || a == "--version" {
			return a, nil
		}
	}
	return "", nil
}

// flagErrorCode returns 0 when the user only asked for help, 2 otherwise.
func flagErrorCode(err error) int {
	if err == flag.ErrHelp {
		return 0
	}
	return 2
}

type pluginsOptions struct {
	renamePlugins bool
}

func parsePluginsFlags(args []string, out io.Writer) (pluginsOptions, error) {
	fs := flag.NewFlagSet("pluginspackage", flag.ContinueOnError)
	fs.SetOutput(out)
	disableRename := fs.Bool("renamepluginsdisabled", false,
		"Disable renaming of plugins to preferred names. Renaming removes older plugins with the same package name.")
	if err := fs.Parse(args); err != nil {
		return pluginsOptions{}, err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(out, "unexpected arguments: %v\n", fs.Args())
		return pluginsOptions{}, fmt.Errorf("unexpected arguments")
	}
	return pluginsOptions{renamePlugins: !*disableRename}, nil
}

type dataPackageOptions struct {
	name, uid, ext                   string
	deleteOnReceive, importOnReceive bool
}

func parseDataPackageFlags(args []string, out io.Writer) (dataPackageOptions, error) {
	var o dataPackageOptions
	fs := flag.NewFlagSet("datapackage", flag.ContinueOnError)
	fs.SetOutput(out)

	// -dbname/-dbuid/-dbext are the documented names. The -dp* names were the
	// only ones the old manual parser understood, so they are kept as aliases.
	fs.StringVar(&o.name, "dbname", "", "Set data package name (default is current directory name)")
	fs.StringVar(&o.uid, "dbuid", "", "Set data package UID (default is randomly generated)")
	fs.StringVar(&o.ext, "dbext", "dpk", "Set data package file extension")
	fs.StringVar(&o.name, "dpname", "", "Same as -dbname")
	fs.StringVar(&o.uid, "dpuid", "", "Same as -dbuid")
	fs.StringVar(&o.ext, "dpext", "dpk", "Same as -dbext")
	fs.BoolVar(&o.deleteOnReceive, "deleteonreceive", false, "Set data package \"onReceiveDelete\" to delete the package after receive")
	fs.BoolVar(&o.importOnReceive, "importonreceive", false, "Set data package \"onReceiveImport\" to import the package after receive")

	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(out, "unexpected arguments: %v\n", fs.Args())
		return o, fmt.Errorf("unexpected arguments")
	}
	return o, nil
}

func parseRepoFlags(args []string, out io.Writer) (repoOptions, error) {
	var o repoOptions
	fs := flag.NewFlagSet("repo", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.config, "config", configFileName, "Configuration file; the apks are read from its folder")
	fs.BoolVar(&o.dryRun, "dry-run", false, "Show what would be done without changing anything")
	fs.BoolVar(&o.clean, "clean", false,
		"Also remove apks that are certainly not needed from the apk folder and apks/removed/ (copies of distributed apks, replaced apks in apks/removed/, Git LFS pointers)")
	fs.BoolVar(&o.cleanAll, "cleanall", false,
		"Remove every apk from the apk folder and apks/removed/ that is not moved into the update folder (also excluded ones and the other ATAK variant)")
	fs.BoolVar(&o.yes, "yes", false, "Apply the changes without asking for confirmation (for scripts)")
	fs.BoolVar(&o.verbose, "v", false, "Verbose: also show the full summary, every apk left in the apk folder, other PDFs and icon notes")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(out, "unexpected arguments: %v\n", fs.Args())
		return o, fmt.Errorf("unexpected arguments")
	}
	return o, nil
}

type checkOptions struct {
	config string
	dir    string
}

func parseCheckFlags(args []string, out io.Writer) (checkOptions, error) {
	var o checkOptions
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprintf(out, "Usage: taktool check [-config file] [update folder]\n\n")
		fmt.Fprintf(out, "The update folder defaults to 'output' of %s, or ./update.\n", configFileName)
		fs.PrintDefaults()
	}
	fs.StringVar(&o.config, "config", "", "Configuration file (default "+configFileName+" if it exists)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	switch fs.NArg() {
	case 0:
	case 1:
		o.dir = fs.Arg(0)
	default:
		fmt.Fprintf(out, "unexpected arguments: %v\n", fs.Args()[1:])
		return o, fmt.Errorf("unexpected arguments")
	}
	return o, nil
}

func runCheck(o checkOptions, stdout, stderr io.Writer) int {
	var cfg *Config
	configPath := o.config
	if configPath == "" {
		if _, err := os.Stat(configFileName); err == nil {
			configPath = configFileName
		}
	}
	if configPath != "" {
		c, err := loadConfig(configPath)
		if err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		cfg = c
	}

	dir := o.dir
	if dir == "" {
		dir = "update"
		if cfg != nil {
			dir = cfg.Output
		}
	}

	errs, err := CheckRepository(stdout, dir, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	if errs > 0 {
		return 1
	}
	return 0
}

func parseManualsFlags(args []string, out io.Writer) (manualsOptions, error) {
	var o manualsOptions
	fs := flag.NewFlagSet("manuals", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.config, "config", configFileName, "Configuration file")
	fs.BoolVar(&o.dryRun, "dry-run", false, "Show what would be copied without copying")
	fs.BoolVar(&o.verbose, "v", false, "Verbose: also list the other PDFs that are not copied")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(out, "unexpected arguments: %v\n", fs.Args())
		return o, fmt.Errorf("unexpected arguments")
	}
	return o, nil
}
