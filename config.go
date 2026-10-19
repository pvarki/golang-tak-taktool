package main

import (
	"fmt"
	"os"
	"path/filepath"
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

	// targets resolved to version lists
	targets map[string][]string

	// Dir is the directory of the config file: the apk folder.
	Dir string `yaml:"-"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.UnmarshalWithOptions(data, &c, yaml.DisallowUnknownField()); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
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

// wantsPackage reports whether the package is distributed.
func (c *Config) wantsPackage(pkg string) bool {
	return len(c.Packages) == 0 || slices.Contains(c.Packages, pkg)
}
