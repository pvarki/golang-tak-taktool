# TAKTOOL

*taktool* is a CLI tool to automate selected TAK-related tasks such as:
- Creating and packaging a data package manifest
- Extracting and adding information from update server APK files to the product.infz package
- Building TAK Server's update folder for several ATAK versions at once, and checking it

## Notes and limitations
- The tool has been tested with limited data. No 100% functionality is guaranteed.
- The tool cannot convert APK file XML icons to PNG format and replace them with a blank image.
- The tool does not consider the icon size and copies it as is.

## Build and install

1. Build with latest go **or** build with `docker compose up` (edit docker-compose.yml to your needs)

```bash
# Linux amd64:
GOOS=linux GOARCH=amd64 go build -o taktool
```

```bash
# Linux arm:
GOOS=linux GOARCH=arm go build -o taktool
```

```bash
# Windows amd64:
GOOS=windows GOARCH=amd64 go build -o taktool.exe
```

Installation / Deploy the binary as follows:
```bash
# Linux:
sudo cp taktool /usr/local/bin && sudo chmod +x /usr/local/bin/taktool
```

Windows: copy taktool.exe to an any installation directory and add the path to PATH

## Usage

Run the tool in the plugins directory (pluginspackage) or in the directory whose contents you want to pack (datapackage).

If you have custom images for the plugins, create images-directory and name like "atak_app.apk" -> "atak_app.png"

```bash
Usage:  taktool COMMAND [OPTIONS]

Commands:
  pluginspackage, pp    Create plugins package
  datapackage, dp       Create data package
  repo                  Build the update folder for several ATAK versions (uses taktool.yaml)
  check                 Check an update folder and show the distributed versions

Run 'taktool COMMAND -h' to see the options of a command.
```

### pluginspackage (pp)

```bash
  -renamepluginsdisabled
        Disable renaming of plugins to preferred names. Renaming removes older plugins with the same package name.
```

By default the apk files are renamed to `<label>_<app|plugin>.apk` (e.g. `atak_app.apk`, `vns_plugin.apk`) and, if the
directory has several apks with the same package name, only the one with the highest revision code (versionCode) is kept
and the others are **deleted**. Nothing is deleted or renamed if the versions cannot be compared reliably (same revision
code but different content) or if two apks would get the same file name.

The "os requirement" field of product.inf is the apk's minSdkVersion (minimum Android API level), as in
the product.inf TAK builds for the apps bundled with ATAK. ATAK shows it in the app details (green if the device
meets it, yellow if not); it does not block installing. It is 1 if the apk has no minSdkVersion.

### datapackage (dp)

```bash
  -dbext string
        Set data package file extension (default "dpk")
  -dbname string
        Set data package name (default is current directory name)
  -dbuid string
        Set data package UID (default is randomly generated)
  -deleteonreceive
        Set data package "onReceiveDelete" to delete the package after receive
  -importonreceive
        Set data package "onReceiveImport" to import the package after receive
```

All files in the current directory and its subdirectories are packed, except the package being created.

Examples:

```bash
taktool pp
taktool dp -dbname="Mission 1" -importonreceive
```

## Update folder for several ATAK versions (repo, check)

TAK Server serves the plugin updates from its `update` folder (`/opt/tak/webcontent/update`):

```
update/
├── repositories.inf    for TAK Server: the ATAK versions that are distributed
├── 5.6.0/
│   ├── atak_app.apk
│   ├── vns_plugin.apk
│   └── product.infz    for ATAK: the apps and plugins offered for this
│                       ATAK version, and their icons
└── 5.8.0/
    └── ...
```

The folder names are free: taktool recognises an update folder by its structure (`repositories.inf` and
`x.y.z` version folders), so it can be called `update`, `plugins` or anything else, and so can the apk folder
described below. Only on the server the name is fixed: ATAK requests `https://<server>:8443/update/...`, so the
folder TAK Server serves must be `webcontent/update`.

`repositories.inf` has one ATAK version per line, in ascending order:

```
5.6.0
5.7.0
5.8.0
```

`product.infz` is a zip with `product.inf` (one comma separated line per apk, see `createProductInf`) and an
icon `<apk name>.png` for each apk.

### repo

Put the new apks (with any file names) and `taktool.yaml` into one folder and run `taktool repo` there.
Custom icons go into an `images` folder next to them (`vns_plugin.png` etc.) and are used for every version.

```yaml
# taktool.yaml
# ATAK versions the server distributes (written to repositories.inf)
versions:
  - 5.6.0
  - 5.7.0
  - 5.8.0

# The update folder (any name). Relative paths are relative to this file.
# Default: update
output: /opt/tak/webcontent/update

# Optional. If given, only these packages are distributed; other apks are
# left in the apk folder. Without it every apk is distributed.
packages:
  - com.atakmap.app.civ
  - com.atakmap.android.datasync.plugin
  - com.atakmap.android.vns.plugin
  - io.opentakserver.opentakicu

# Which ATAK is distributed: small (default) or full. See "ATAK small and full".
atak: small

# Optional. Copy the PDF manuals of the distributed apks into this folder
# (any name, flat, not inside the update folder). See "Manuals".
manuals: ../manuals
# Also copy the other PDFs found in the apks, with their own names
allmanuals: false

# Optional. The ATAK versions a package is distributed to, instead of the
# version found in the apk: "all", one version or a list of versions.
targets:
  io.opentakserver.opentakicu: all
  com.example.foo.plugin: [5.7.0, 5.8.0]
```

`taktool.yaml` is kept in the apk folder and is the description of what the server distributes: change it to
change the distribution. If `packages` is given, packages that are not listed are also removed from the update
folder.

```bash
  -cleansource
        Also remove apks that are certainly not needed from the apk folder (older revisions, copies, Git LFS pointers)
  -config string
        Configuration file; the apks are read from its folder (default "taktool.yaml")
  -dry-run
        Show what would be done without changing anything
  -yes
        Apply the changes without asking for confirmation (for scripts)
```

What `taktool repo` does:

- Finds the ATAK version each apk is made for, in this order: the `plugin-api` value in the apk
  (`com.atakmap.app@5.8.0.CIV`), the end of its versionName (`4.0 (ac524e2c) - [5.8.0]`), and its file name
  (`ATAK-Plugin-vns-4.2-4bb952de-5.6.0-civ-release.apk`). Apps that are not made for a specific ATAK version
  (e.g. OpenTAK ICU) go into every version folder. `targets` in `taktool.yaml` overrides this for a package;
  a plugin whose ATAK version cannot be found is left in the apk folder until it has a target.
- Only CIV products are supported. MIL and GOV products are left in the apk folder with a warning.
- Moves the apks into the version folders. Version folders are updated incrementally: apks already in the
  update folder stay unless a newer revision (versionCode) of the same package replaces them. A warning is shown
  if the new apk is signed with a different certificate than the one it replaces (devices cannot update it).
- Renames the apks (`vns_plugin.apk`, ...) and writes `product.infz` for each version, like `taktool pp`.
  An existing version folder is also rebuilt when its apks have other names or its `product.infz` differs from
  what taktool would write now (e.g. one written by an older taktool, or with other custom icons).
- Builds each version folder in a temporary folder and swaps it in place when it is complete.
- Writes `repositories.inf` and **deletes everything else** in the update folder: version folders not in
  `versions`, `repository.inf` and any other files. Hidden files such as `.keep` are kept. To protect against a
  wrong `output`, a non-empty folder without `repositories.inf`, `repository.inf` or version folders is refused.
- Apks that were not distributed stay in the apk folder, and the report says why (e.g. not in `versions` or
  `packages`). Add them to `taktool.yaml` or remove them. `-cleansource` removes the ones that are certainly not
  needed.

### ATAK small and full

TAK.gov publishes two ATAK CIV apks of each version:

| | full (`ATAK-...-civ-release.apk`) | small (`ATAK-...-civSmall-release.apk`) |
|---|---|---|
| Size | about 365 MB | about 110 MB |
| Native libraries | arm64-v8a, armeabi-v7a (32-bit ARM), x86, x86_64 | arm64-v8a only |
| Bundled apps | 7 (TAK GeoCam, HelmCam, TAK ICU, Image Markup, Network Monitor, Night Vision, Serial Monitor) | none |
| versionName | `5.8.0.5 (a306924)` | `5.8.0.5 (a306924)[playstore]` |

The package name, versionCode and signing certificate are the same. The small apk cannot be installed on a
32-bit device; use `atak: full` for those.

`taktool repo` tells them apart by content (32-bit or x86 libraries or bundled apps mean full) and warns if
the versionName disagrees. It distributes the variant set by `atak`. If the apk folder or the version folder only
has the other variant for a version, that one is distributed with a warning. Because both variants have the
same versionCode, changing `atak` replaces the apk in the update folder, but devices that already have that
ATAK version will probably not be offered the other variant as an update.

### Manuals

With `manuals` in `taktool.yaml`, `taktool repo` copies the manuals bundled in the distributed apks into that
folder:

| From | Named |
|---|---|
| plugin: `assets/usermanual.pdf` | `ATAK 5.8 VNS 4.0 UserGuide.pdf` |
| ATAK: `assets/support/docs/*.pdf` | `ATAK 5.8 UserGuide.pdf`, `ATAK 5.8 IconGlossary.pdf` |

The names follow `<Product> <TAK version> <Plugin> <plugin version> <DocType>.pdf`; the plugin name is the
label without spaces (`Data Sync` -> `DataSync`). A manual is written only if it is missing or its content has
changed; nothing is deleted from the folder.

Other PDFs in an apk may be outdated (Data Sync 5.0.2 also bundles the guide of version 3.5), so they are only
listed in the report. With `allmanuals: true` they are copied too, as `<Plugin> <original name>.pdf`.

The manuals folder must not be inside the update folder: TAK Server would serve it to every device.

Before changing anything, `taktool repo` shows the report and asks for confirmation. Without a terminal
(scripts, cron) it does not change anything unless `-yes` is given. If you are not sure, run `-dry-run` first.
Example report:

```
Changes:
  5.8.0: new version folder, add ATAK 5.8.0.3, add VNS 4.0, add Data Sync 5.0.2, add OpenTAK ICU 1.5.4
  5.6.0: remove (not in versions)
  remove repository.inf

Summary:
  5.7.0: Data Sync 4.0.5, OpenTAK ICU 1.5.4, VNS 4.0
  5.8.0: ATAK 5.8.0.3, Data Sync 5.0.2, OpenTAK ICU 1.5.4, VNS 4.0

Left in the apk folder:
  ATAK-Plugin-datasync-3.7.4-5.6.0-civ-release.apk: made for ATAK 5.6.0 (from plugin-api), which is not in versions
  vns-copy.apk: already in 5.7.0 [-cleansource would remove]

Warnings:
  5.7.0: no ATAK apk
```

### check

```bash
taktool check [-config taktool.yaml] [update folder]
```

Shows the distributed versions (the Summary above) and checks that:

- every version in `repositories.inf` has a folder, and every version folder is listed
- every apk listed in `product.inf` exists with the size and sha256 given there, and its icon is in
  `product.infz`; apks that `product.inf` does not list are reported
- no file is a Git LFS pointer (left when a repository using Git LFS is cloned without the LFS files)
- every package is in every version: the `packages` of `taktool.yaml` if there is one, otherwise every package
  found in any version
- the same package is signed with the same certificate in every version
- the distributed ATAK variant (shown in the summary) matches `atak` in `taktool.yaml`

If `taktool.yaml` exists in the current folder, its `output` is checked by default. The exit status is 1 if
errors were found, so the command can be used in CI.
