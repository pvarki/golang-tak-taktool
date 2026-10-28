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

Update folder for TAK Server (several ATAK versions, uses taktool.yaml):
  init                 Create the folders and taktool.yaml
  repo                 Build the update folder from the apks
  check                Check an update folder and show what it distributes
  manuals              Copy the manuals of the distributed apks

Single folder:
  pluginspackage, pp   Create product.infz for the apks in this folder
  datapackage, dp      Create a data package of this folder

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

## Update folder for several ATAK versions (init, repo, check, manuals)

### Quick start

```bash
taktool init taktool-work         # creates apks/ (taktool.yaml, images/), update/ and manuals/
cd taktool-work/apks              # put the apks from TAK.gov here
                                  # edit taktool.yaml: versions, output, exclude ...
taktool repo -dry-run             # shows what would be done
taktool repo                      # does it (asks for confirmation)
taktool check                     # checks the update folder and shows what it distributes
taktool manuals                   # optional: copies the PDF manuals into manuals/
```

Then copy the update folder to the server (`/opt/tak/webcontent/update`), or set `output` to it directly.
`taktool init` never changes anything that exists already.

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
[`taktool.example.yaml`](taktool.example.yaml) is a commented template with every option.
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

# Optional. For 'taktool manuals'. See "Manuals".
manuals:
  folder: ../manuals    # any name, flat, not inside the update folder
  all: false            # also copy the other PDFs found in the apks
  withrepo: false       # also copy the manuals on every 'taktool repo'

# Optional. Apks that are never distributed: glob patterns (* and ?,
# case-insensitive) matched against file names and package names
exclude:
  - "*UASTool-DIUBLUE*"
  - com.atakmap.android.atakgo.dji

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
  -clean
        Also remove apks that are certainly not needed from the apk folder (older revisions, copies, Git LFS pointers)
        (-cleansource is an old name for the same)
  -cleanall
        Remove every apk from the apk folder that is not moved into the update folder (also excluded ones and the other ATAK variant)
  -config string
        Configuration file; the apks are read from its folder (default "taktool.yaml")
  -dry-run
        Show what would be done without changing anything
  -v    Verbose: also show the full summary, every apk left in the apk folder, other PDFs and icon notes
  -yes
        Apply the changes without asking for confirmation (for scripts)
```

What `taktool repo` does:

- Finds the ATAK version each apk is made for, in this order: the `plugin-api` value in the apk
  (`com.atakmap.app@5.8.0.CIV`), the end of its versionName (`4.0 (ac524e2c) - [5.8.0]`), and its file name
  (`ATAK-Plugin-vns-4.2-4bb952de-5.6.0-civ-release.apk`). Apps that are not made for a specific ATAK version
  (e.g. OpenTAK ICU) go into every version folder. `targets` in `taktool.yaml` overrides this for a package;
  a plugin whose ATAK version cannot be found is left in the apk folder until it has a target.
- Apks matching `exclude` stay in the apk folder. Special builds that have the same package name as the normal
  one (UAS Tool DIU Blue, WAVE Legacy) can only be told apart by file name, so exclude them by file name;
  otherwise the one with the highest revision would be distributed.
- Only CIV products are supported. MIL and GOV products are left in the apk folder with a warning.
- Moves the apks into the version folders. Version folders are updated incrementally: apks already in the
  update folder stay unless a newer revision (versionCode) of the same package replaces them. A warning is shown
  if the new apk is signed with a different certificate than the one it replaces (devices cannot update it).
- Sets the type (app or plugin) as ATAK does: an apk is a plugin if it has the `plugin-api` meta-data and is not
  ATAK itself; the package name does not matter (Beartooth's package is `com.beartooth.beartoothtakplugin`).
- Renames the apks (`vns_plugin.apk`, ...) and writes `product.infz` for each version, like `taktool pp`.
  An existing version folder is also rebuilt when its apks have other names or its `product.infz` differs from
  what taktool would write now (e.g. one written by an older taktool, or with other custom icons).
- Builds each version folder in a temporary folder and swaps it in place when it is complete.
- Writes `repositories.inf` and **deletes everything else** in the update folder: version folders not in
  `versions`, `repository.inf` and any other files. Hidden files such as `.keep` are kept. To protect against a
  wrong `output`, a non-empty folder without `repositories.inf`, `repository.inf` or version folders is refused.
- Apks that were not distributed stay in the apk folder, and the report says why (e.g. not in `versions` or
  `packages`). Add them to `taktool.yaml` or remove them. `-clean` removes the ones that are certainly not
  needed; `-cleanall` removes all of them (only apk files; `taktool.yaml`, `images/` and other files stay).

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

`taktool manuals` copies the manuals bundled in the apks of the update folder into `manuals: folder:` of
`taktool.yaml`. Run it in the apk folder after `taktool repo`. With `withrepo: true`, `taktool repo` also does
it every time. Options: `-dry-run`, `-v` (also lists the other PDFs) and `-config`.

| From | Named |
|---|---|
| plugin: `assets/usermanual.pdf` | `ATAK 5.8 VNS 4.0 UserGuide.pdf` |
| ATAK: `assets/support/docs/*.pdf` | `ATAK 5.8 UserGuide.pdf`, `ATAK 5.8 IconGlossary.pdf` |

The names follow `<Product> <TAK version> <Plugin> <plugin version> <DocType>.pdf`; the plugin name is the
label without spaces (`Data Sync` -> `DataSync`). An app that is distributed to several ATAK versions as the same
apk (e.g. HelmCam) gets one manual, named after the highest version. A manual is written only if it is missing or its content has
changed; nothing is deleted from the folder.

Other PDFs in an apk may be outdated (Data Sync 5.0.2 also bundles the guide of version 3.5), so they are only
listed with `-v`. With `all: true` they are copied too, as `<Plugin> <original name>.pdf`.

The manuals folder must not be inside the update folder: TAK Server would serve it to every device.

Before changing anything, `taktool repo` shows the report and asks for confirmation. Without a terminal
(scripts, cron) it does not change anything unless `-yes` is given. If you are not sure, run `-dry-run` first.
Example report:

```
Changes:

5.6.0
  Added:
    WAVE 2.1
  Updated:
    VNS 4.0 -> 4.2

5.8.0 (new version folder)
  Added:
    ATAK 5.8.0.5 (small), Data Sync 5.0.2, VNS 4.2

Removed from the update folder:
    5.5.0 (not in versions), repository.inf

Left in the apk folder:
  ATAK-Plugin-foo-1.0-5.5.0-civ-release.apk: made for ATAK 5.5.0 (from plugin-api), which is not in versions
  4 apks left as expected (excluded, the other ATAK variant, older or already distributed); -v lists them

Icons: 2 images/ files are the same as the apk's own icon and not needed; -v lists them
```

By default the report shows only what changes and what needs attention. `-v` also shows the full summary per
version, every apk left in the apk folder, the other PDFs in the apks and the icon notes. `taktool check` always
shows the summary.

### Icons

ATAK shows a png icon for each apk in the list of available apps. taktool takes the icon from the apk: a png
icon as it is, and WebP icons, vector icons, layer lists and adaptive icons converted to a 144x144 png. If an
icon cannot be read or drawn, the app gets an empty icon.

To use another icon, put a png into an `images` folder in the apk folder, named after the app's short name,
the label without spaces as in the manual names (`images/datasync.png` for Data Sync; case, `-` and `_` are
ignored). The package name (`images/com.atakmap.android.datasync.plugin.png`) and the apk name in the update
folder (`images/data_sync_plugin.png`) also work and take precedence. A custom image is always used instead of
the apk's icon.

The report says how many apps have no usable icon, how many custom images differ from the apk's own icon (the
apk's icon may be newer) and how many are the same as it and not needed. `taktool repo -dry-run -v` lists them
and the icons that were converted.

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
