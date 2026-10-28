package main

import (
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
)

const configFileName = "taktool.yaml"

// Config is the content of taktool.yaml. The file is kept in the apk folder
// together with the apk files. Example:
//
//	# ATAK versions the server distributes. Written to repositories.inf.
//	versions:
//	  - 5.6.0
//	  - 5.7.0
//	  - 5.8.0
//
//	# The update folder (any name). Relative paths are relative to this file.
//	# Default: update
//	output: /opt/tak/webcontent/update
//
//	# Optional. If given, only these packages are distributed; other apks are
//	# left in the apk folder.
//	packages:
//	  - com.atakmap.app.civ
//	  - com.atakmap.android.datasync.plugin
//	  - com.atakmap.android.vns.plugin
//
//	# Which ATAK is distributed: small (civSmall-release, default) or full
//	# (civ-release: also 32-bit devices, bundled apps, but about 3x larger)
//	atak: small
//
//	# Optional. For 'taktool manuals': copy the PDF manuals of the
//	# distributed apks into folder (any name, not inside the update folder).
//	# all: true also copies the other PDFs found in the apks, with their own
//	# names. withrepo: true also copies them on every 'taktool repo'.
//	manuals:
//	  folder: ../manuals
//	  all: false
//	  withrepo: false
//
//	# Optional. Apks that are never distributed: glob patterns (* and ?,
//	# case-insensitive) matched against the file names in the apk folder
//	# and against package names.
//	exclude:
//	  - "*UASTool-DIUBLUE*"
//	  - com.atakmap.android.atakgo.dji
//
//	# Optional. The ATAK versions a package is distributed to, instead of the
//	# version found in the apk: "all", one version or a list of versions.
//	targets:
//	  io.opentakserver.opentakicu: all
//	  com.example.foo.plugin: [5.7.0, 5.8.0]
type Config struct {
	Versions []string       `yaml:"versions"`
	Output   string         `yaml:"output"`
	Packages []string       `yaml:"packages"`
	Targets  map[string]any `yaml:"targets"`
	Atak     string         `yaml:"atak"`
	Manuals  ManualsConfig  `yaml:"manuals"`
	Exclude  []string       `yaml:"exclude"`

	// targets resolved to version lists
	targets map[string][]string

	// Dir is the directory of the config file: the apk folder.
	Dir string `yaml:"-"`
}

// ManualsConfig is the manuals section of taktool.yaml.
type ManualsConfig struct {
	Folder   string `yaml:"folder"`
	All      bool   `yaml:"all"`
	WithRepo bool   `yaml:"withrepo"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%s not found; run 'taktool init' to create it, or give its path with -config", path)
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.UnmarshalWithOptions(data, &c, yaml.DisallowUnknownField()); err != nil {
		return nil, configSyntaxError(path, data, err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.Dir = filepath.Dir(abs)

	if len(c.Versions) == 0 {
		return nil, fmt.Errorf("%s: no versions given", path)
	}
	var versions []string
	for _, v := range c.Versions {
		v = strings.TrimSpace(v)
		if !versionRe.MatchString(v) {
			return nil, fmt.Errorf("%s: version %q is not in the form 5.8.0", path, v)
		}
		if !slices.Contains(versions, v) {
			versions = append(versions, v)
		}
	}
	slices.SortFunc(versions, compareVersions)
	c.Versions = versions

	if c.Output == "" {
		c.Output = "update"
	}
	if !filepath.IsAbs(c.Output) {
		c.Output = filepath.Join(c.Dir, c.Output)
	}
	c.Output = filepath.Clean(c.Output)

	var packages []string
	for _, p := range c.Packages {
		p = strings.TrimSpace(p)
		if p != "" && !slices.Contains(packages, p) {
			packages = append(packages, p)
		}
	}
	c.Packages = packages

	switch c.Atak = strings.ToLower(strings.TrimSpace(c.Atak)); c.Atak {
	case "":
		c.Atak = atakSmall
	case atakSmall, atakFull:
	default:
		return nil, fmt.Errorf("%s: atak: %q, use small or full", path, c.Atak)
	}

	if m := &c.Manuals; m.Folder != "" {
		if !filepath.IsAbs(m.Folder) {
			m.Folder = filepath.Join(c.Dir, m.Folder)
		}
		m.Folder = filepath.Clean(m.Folder)
		if rel, err := filepath.Rel(c.Output, m.Folder); err == nil && !strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("%s: manuals: folder must not be inside the update folder %s (TAK Server would serve it to every device)", path, c.Output)
		}
	} else if m.All || m.WithRepo {
		return nil, fmt.Errorf("%s: manuals: all and withrepo need folder (where the manuals are copied)", path)
	}

	for i, pattern := range c.Exclude {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if _, err := pathpkg.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("%s: exclude: %q: %w", path, pattern, err)
		}
		c.Exclude[i] = pattern
	}

	c.targets = map[string][]string{}
	for pkg, value := range c.Targets {
		versions, err := c.parseTarget(value)
		if err != nil {
			return nil, fmt.Errorf("%s: targets: %s: %w", path, pkg, err)
		}
		if !c.wantsPackage(pkg) {
			return nil, fmt.Errorf("%s: targets: %s is not in packages", path, pkg)
		}
		c.targets[pkg] = versions
	}

	return &c, nil
}

// parseTarget turns a value of targets ("all", "5.8.0" or a list of
// versions) into a list of versions.
func (c *Config) parseTarget(value any) ([]string, error) {
	var list []string
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "all" {
			return c.Versions, nil
		}
		list = []string{v}
	case []any:
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("%v is not a version", x)
			}
			list = append(list, s)
		}
	default:
		return nil, fmt.Errorf(`use "all", a version or a list of versions`)
	}

	var versions []string
	for _, s := range list {
		s = strings.TrimSpace(s)
		if !slices.Contains(c.Versions, s) {
			return nil, fmt.Errorf("version %q is not in versions", s)
		}
		if !slices.Contains(versions, s) {
			versions = append(versions, s)
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no versions given")
	}
	slices.SortFunc(versions, compareVersions)
	return versions, nil
}

// targetVersions returns the versions taktool.yaml sets for a package.
func (c *Config) targetVersions(pkg string) ([]string, bool) {
	v, ok := c.targets[pkg]
	return v, ok
}

// excluded reports whether a file name or package name matches a pattern of
// exclude, and returns the pattern.
func (c *Config) excluded(name string) (string, bool) {
	name = strings.ToLower(name)
	for _, pattern := range c.Exclude {
		if ok, _ := pathpkg.Match(pattern, name); ok {
			return pattern, true
		}
	}
	return "", false
}

// wantsPackage reports whether the package is distributed.
func (c *Config) wantsPackage(pkg string) bool {
	return len(c.Packages) == 0 || slices.Contains(c.Packages, pkg)
}

// configSyntaxError explains a taktool.yaml that cannot be read and how to
// fix it.
func configSyntaxError(path string, data []byte, err error) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s has an error:\n%s\n\n", path, strings.TrimRight(err.Error(), "\n "))
	if oldManualsRe.Match(data) {
		b.WriteString("manuals is a group of settings:\n" +
			"  manuals:\n    folder: ../manuals\n    all: false\n    withrepo: false\n\n")
	}
	initCmd := "taktool init"
	if abs, e := filepath.Abs(path); e == nil && strings.EqualFold(filepath.Base(filepath.Dir(abs)), "apks") {
		initCmd = "taktool init .." // init creates apks/taktool.yaml in its folder
	}
	fmt.Fprintf(&b, "Fix it as in taktool.example.yaml (indent with spaces, not tabs), or rename it\n"+
		"and create a new one: '%s' (nothing that exists is changed).", initCmd)
	return errors.New(b.String())
}

// oldManualsRe matches the forms of the 2.0 test builds, "manuals: <folder>"
// and "allmanuals:".
var oldManualsRe = regexp.MustCompile(`(?m)^(manuals:[ \t]*[^\s#]|allmanuals:)`)
