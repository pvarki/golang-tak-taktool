package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func usage(w io.Writer) {
	fmt.Fprintf(w, "Usage:  taktool COMMAND [OPTIONS]\n\n")
	fmt.Fprintf(w, "Commands:\n")
	fmt.Fprintf(w, "  pluginspackage, pp\tCreate plugins package\n")
	fmt.Fprintf(w, "  datapackage, dp\tCreate data package\n")
	fmt.Fprintf(w, "  repo\t\t\tBuild the update folder for several ATAK versions (uses %s)\n", configFileName)
	fmt.Fprintf(w, "  check\t\t\tCheck an update folder and show the distributed versions\n\n")
	fmt.Fprintf(w, "Run 'taktool COMMAND -h' to see the options of a command.\n")
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
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
		if a == "-h" || a == "-help" || a == "--help" {
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
	fs.BoolVar(&o.cleanSource, "cleansource", false,
		"Also remove apks that are certainly not needed from the apk folder (older revisions, copies, Git LFS pointers)")
	fs.BoolVar(&o.yes, "yes", false, "Apply the changes without asking for confirmation (for scripts)")
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
