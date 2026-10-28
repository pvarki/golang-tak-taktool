package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// apkFile creates a dummy file standing in for an apk and returns its name.
func apkFile(t *testing.T, name string) string {
	t.Helper()
	if err := os.WriteFile(name, []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

func filesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestRemoveOlderPluginVersions(t *testing.T) {
	t.Chdir(t.TempDir())

	// The old implementation panicked (index -1) on exactly this input
	in := []ApkInfo{
		{Package: "com.a.plugin", DisplayName: "A", Revision: "1", Hash: "a1", ApkPath: apkFile(t, "a1.apk")},
		{Package: "com.a.plugin", DisplayName: "A", Revision: "2", Hash: "a2", ApkPath: apkFile(t, "a2.apk")},
		{Package: "com.b.plugin", DisplayName: "B", Revision: "1", Hash: "b", ApkPath: apkFile(t, "b.apk")},
		{Package: "com.c.plugin", DisplayName: "C", Revision: "1", Hash: "c", ApkPath: apkFile(t, "c.apk")},
		{Package: "com.a.plugin", DisplayName: "A", Revision: "3", Hash: "a3", ApkPath: apkFile(t, "a3.apk")},
		{Package: "com.c.plugin", DisplayName: "C", Revision: "1", Hash: "c", ApkPath: apkFile(t, "c-copy.apk")},
	}

	out, err := RemoveOlderPluginVersions(in)
	if err != nil {
		t.Fatal(err)
	}

	var kept []string
	for _, a := range out {
		kept = append(kept, a.ApkPath)
	}
	if want := []string{"b.apk", "c.apk", "a3.apk"}; !slices.Equal(kept, want) {
		t.Errorf("kept %v, want %v", kept, want)
	}
	if got, want := filesIn(t, "."), []string{"a3.apk", "b.apk", "c.apk"}; !slices.Equal(got, want) {
		t.Errorf("files on disk %v, want %v", got, want)
	}
}

func TestRemoveOlderKeepsDifferentPackagesWithSameLabel(t *testing.T) {
	t.Chdir(t.TempDir())
	in := []ApkInfo{
		{Package: "com.atakmap.app.civ", DisplayName: "ATAK", Type: "app", Revision: "5", ApkPath: apkFile(t, "civ.apk")},
		{Package: "com.atakmap.app.mil", DisplayName: "ATAK", Type: "app", Revision: "3", ApkPath: apkFile(t, "mil.apk")},
	}
	out, err := RemoveOlderPluginVersions(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || len(filesIn(t, ".")) != 2 {
		t.Errorf("both apks should be kept, got %d infos and files %v", len(out), filesIn(t, "."))
	}
}

func TestRemoveOlderDeletesNothingWhenUnsure(t *testing.T) {
	tests := map[string][2]ApkInfo{
		"same revision, different content": {
			{Package: "p", Revision: "7", Hash: "x", ApkPath: "x.apk"},
			{Package: "p", Revision: "7", Hash: "y", ApkPath: "y.apk"},
		},
		"revision not a number": {
			{Package: "p", Revision: "7", Hash: "x", ApkPath: "x.apk"},
			{Package: "p", Revision: "abc", Hash: "y", ApkPath: "y.apk"},
		},
	}
	for name, pair := range tests {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			apkFile(t, "x.apk")
			apkFile(t, "y.apk")
			if _, err := RemoveOlderPluginVersions(pair[:]); err == nil {
				t.Error("expected an error")
			}
			if n := len(filesIn(t, ".")); n != 2 {
				t.Errorf("%d files left, want 2 (nothing deleted)", n)
			}
		})
	}
}

func TestReworkPluginName(t *testing.T) {
	tests := map[string]string{
		// names currently used in pvarki/docker-atak-server
		"ATAK_app":         "atak_app",
		"OpenTAK ICU_app":  "opentak_icu_app",
		"Data Sync_plugin": "data_sync_plugin",
		"VNS_plugin":       "vns_plugin",
		// duplicate suffixes
		"Foo Plugin_plugin": "foo_plugin",
		"Bar App_app":       "bar_app",
		// characters that are not safe in file names
		"A/B: C?_plugin": "a_b__c__plugin",
		"Sää 2.0_app":    "sää_2_0_app",
	}
	for in, want := range tests {
		if got := reworkPluginName(in); got != want {
			t.Errorf("reworkPluginName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenamePlugins(t *testing.T) {
	t.Chdir(t.TempDir())
	in := []ApkInfo{
		{Package: "com.atakmap.android.vns.plugin", DisplayName: "VNS", Type: "plugin", ApkPath: apkFile(t, "VNS-4.0.apk")},
		{Package: "com.x", DisplayName: "", Type: "app", ApkPath: apkFile(t, "x.apk")},
	}
	out, err := RenamePlugins(in)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].ApkPath != "vns_plugin.apk" || out[1].ApkPath != "com_x_app.apk" {
		t.Errorf("new names %q, %q", out[0].ApkPath, out[1].ApkPath)
	}
	if got, want := filesIn(t, "."), []string{"com_x_app.apk", "vns_plugin.apk"}; !slices.Equal(got, want) {
		t.Errorf("files on disk %v, want %v", got, want)
	}
}

func TestRenamePluginsCollisionChangesNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	in := []ApkInfo{
		{Package: "com.atakmap.app.civ", DisplayName: "ATAK", Type: "app", ApkPath: apkFile(t, "civ.apk")},
		{Package: "com.atakmap.app.mil", DisplayName: "atak", Type: "app", ApkPath: apkFile(t, "mil.apk")},
	}
	if _, err := RenamePlugins(in); err == nil {
		t.Fatal("expected an error, both would be named atak_app.apk")
	}
	if got, want := filesIn(t, "."), []string{"civ.apk", "mil.apk"}; !slices.Equal(got, want) {
		t.Errorf("files on disk %v, want %v", got, want)
	}
}

func TestRenamePluginsDoesNotOverwrite(t *testing.T) {
	t.Chdir(t.TempDir())
	apkFile(t, "vns_plugin.apk") // some other file already has the target name
	in := []ApkInfo{
		{Package: "com.atakmap.android.vns.plugin", DisplayName: "VNS", Type: "plugin", ApkPath: apkFile(t, "new.apk")},
	}
	if _, err := RenamePlugins(in); err == nil {
		t.Fatal("expected an error")
	}
	b, _ := os.ReadFile("vns_plugin.apk")
	if string(b) != "vns_plugin.apk" {
		t.Error("existing file was overwritten")
	}
}

const testManifestXML = `<manifest versionCode="1781195761" versionName="4.0, (ac524e2c)" package="com.atakmap.android.vns.plugin">
	<uses-sdk minSdkVersion="23" targetSdkVersion="35"></uses-sdk>
	<application label="VNS" icon="res/S5.png">
		<meta-data name="plugin-api" value="com.atakmap.app@5.8.0.CIV"></meta-data>
		<meta-data name="app_desc" value="Vehicle Navigation System
second line"></meta-data>
	</application>
</manifest>`

func TestReadParametersFromXML(t *testing.T) {
	got, err := readParametersFromXML(strings.NewReader(testManifestXML))
	if err != nil {
		t.Fatal(err)
	}
	want := ApkInfo{
		Platform:    "Android",
		Type:        "plugin",
		Package:     "com.atakmap.android.vns.plugin",
		DisplayName: "VNS",
		Version:     "4.0 (ac524e2c)",
		Revision:    "1781195761",
		IconPath:    "res/S5.png",
		Description: "Vehicle Navigation System",
		OsReq:       23, // minSdkVersion
		TakReq:      "com.atakmap.app@5.8.0.CIV",
	}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestReadParametersFromXMLShortPackage(t *testing.T) {
	// The old implementation panicked on package names shorter than 7 characters
	got, err := readParametersFromXML(strings.NewReader(`<manifest package="a.b"></manifest>`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != "app" || got.OsReq != 1 {
		t.Errorf("got type %q, os requirement %d", got.Type, got.OsReq)
	}
}

func TestIsApkFile(t *testing.T) {
	for name, want := range map[string]bool{
		"a.apk": true, "A.APK": true, "a.apk.bak": false, "a.apks": false, filepath.Join("d", "a.apk"): true,
	} {
		if got := isApkFile(name); got != want {
			t.Errorf("isApkFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCustomImageFor(t *testing.T) {
	a := ApkInfo{Package: "com.atakmap.android.wind.plugin"}
	list := []string{"vns_plugin.png", "COM.ATAKMAP.ANDROID.WIND.PLUGIN.png"}
	if got := customImageFor(a, "wind_data_plugin.png", list); got != "COM.ATAKMAP.ANDROID.WIND.PLUGIN.png" {
		t.Errorf("by package name: %q", got)
	}
	if got := customImageFor(ApkInfo{Package: "x"}, "vns_plugin.png", list); got != "vns_plugin.png" {
		t.Errorf("by apk name: %q", got)
	}
	if got := customImageFor(ApkInfo{Package: "y"}, "y_app.png", list); got != "" {
		t.Errorf("none: %q", got)
	}
}

func TestCustomImageShortName(t *testing.T) {
	list := []string{"datasync.png", "MAVSDK-Anafi.PNG", "wind_data.png"}
	for label, want := range map[string]string{
		"Data Sync":    "datasync.png",
		"MAVSDK-Anafi": "MAVSDK-Anafi.PNG",
		"Wind Data":    "wind_data.png",
		"VNS":          "",
	} {
		if got := customImageFor(ApkInfo{Package: "p", DisplayName: label}, "p_app.png", list); got != want {
			t.Errorf("%s: got %q, want %q", label, got, want)
		}
	}
	// The apk name and the package name are preferred over the short name
	list = append(list, "com.atakmap.android.datasync.plugin.png")
	a := ApkInfo{Package: "com.atakmap.android.datasync.plugin", DisplayName: "Data Sync"}
	if got := customImageFor(a, "data_sync_plugin.png", list); got != "com.atakmap.android.datasync.plugin.png" {
		t.Errorf("preference: got %q", got)
	}
}

func TestPluginTypeFromPluginAPI(t *testing.T) {
	for xmlText, want := range map[string]string{
		`<manifest package="com.beartooth.beartoothtakplugin"><application><meta-data name="plugin-api" value="com.atakmap.app@5.8.0.CIV"></meta-data></application></manifest>`: "plugin",
		`<manifest package="com.atakmap.app.civ"><application><meta-data name="plugin-api" value="com.atakmap.app@5.8.0.CIV"></meta-data></application></manifest>`:              "app",
		`<manifest package="com.atakmap.helmcam"><application></application></manifest>`:                                                                                         "app",
		`<manifest package="com.atakmap.android.vns.plugin"><application></application></manifest>`:                                                                              "app", // no plugin-api: ATAK treats it as an app
	} {
		got, err := readParametersFromXML(strings.NewReader(xmlText))
		if err != nil {
			t.Fatal(err)
		}
		if got.Type != want {
			t.Errorf("%s: type %q, want %q", got.Package, got.Type, want)
		}
	}
}
