package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestDataPackageFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want dataPackageOptions
	}{
		{"defaults", nil, dataPackageOptions{ext: "dpk"}},
		{"documented names", []string{"-dbname=foo", "-dbuid=u", "-dbext=zip"}, dataPackageOptions{name: "foo", uid: "u", ext: "zip"}},
		{"value as separate argument", []string{"-dbname", "foo bar"}, dataPackageOptions{name: "foo bar", ext: "dpk"}},
		{"old -dp aliases", []string{"-dpname=foo", "-dpext=.zip"}, dataPackageOptions{name: "foo", ext: ".zip"}},
		{"booleans", []string{"-deleteonreceive", "-importonreceive"}, dataPackageOptions{ext: "dpk", deleteOnReceive: true, importOnReceive: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDataPackageFlags(tt.args, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDataPackageFlagsRejectsUnknown(t *testing.T) {
	if _, err := parseDataPackageFlags([]string{"-nosuchflag"}, io.Discard); err == nil {
		t.Error("expected an error for an unknown flag")
	}
	if _, err := parseDataPackageFlags([]string{"stray"}, io.Discard); err == nil {
		t.Error("expected an error for an extra argument")
	}
}

func TestPluginsFlags(t *testing.T) {
	got, err := parsePluginsFlags(nil, io.Discard)
	if err != nil || !got.renamePlugins {
		t.Errorf("default: got %+v, %v; want renaming on", got, err)
	}
	got, err = parsePluginsFlags([]string{"-renamepluginsdisabled"}, io.Discard)
	if err != nil || got.renamePlugins {
		t.Errorf("disabled: got %+v, %v; want renaming off", got, err)
	}
}

func TestSplitCommand(t *testing.T) {
	cmd, rest := splitCommand([]string{"-renamepluginsdisabled", "pp"})
	if cmd != "pp" || len(rest) != 1 || rest[0] != "-renamepluginsdisabled" {
		t.Errorf("flag before command: got %q %v", cmd, rest)
	}
	cmd, rest = splitCommand([]string{"dp", "-dbname", "x"})
	if cmd != "dp" || len(rest) != 2 {
		t.Errorf("flags after command: got %q %v", cmd, rest)
	}
	if cmd, _ := splitCommand(nil); cmd != "" {
		t.Errorf("no args: got %q", cmd)
	}
}

func TestRunExitCodes(t *testing.T) {
	if code := run(nil, io.Discard, io.Discard); code != 2 {
		t.Errorf("no args: exit %d, want 2", code)
	}
	if code := run([]string{"nosuchcommand"}, io.Discard, io.Discard); code != 2 {
		t.Errorf("unknown command: exit %d, want 2", code)
	}
	if code := run([]string{"dp", "-h"}, io.Discard, io.Discard); code != 0 {
		t.Errorf("dp -h: exit %d, want 0", code)
	}
}

func TestVersionCommand(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"-version"}, {"--version"}} {
		var out, errOut strings.Builder
		if code := run(args, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "taktool "+version) {
			t.Errorf("%v: code %d, output %q", args, code, out.String())
		}
	}
}

func TestMain(m *testing.M) {
	// Tests must not read or write the user's apk cache
	os.Setenv("TAKTOOL_CACHE", "off")
	os.Exit(m.Run())
}
