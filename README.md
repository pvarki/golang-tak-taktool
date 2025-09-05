# TAKTOOL

*taktool* is a CLI tool to automate selected TAK-related tasks such as:
- Creating and packaging a data package manifest
- Extracting and adding information from update server APK files to the product.infz package

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

Run the tool in the plugins directory

If you have custom images for the plugins, create images-directory and name like "atak_app.apk" -> "atak_app.png"

```bash
Usage:  taktool COMMAND [OPTIONS]

Commands:
  pluginspackage, pp    Create plugins package
  datapackage, dp       Create data package

Options:
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
  -renamepluginsdisabled
        Disable renaming of plugins to preferred names. Renaming removes older plugins with the same name.
```


