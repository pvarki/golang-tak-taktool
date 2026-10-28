package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"path"
	"strings"

	"github.com/avast/apkparser"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// convertedIconSize is the size of icons that taktool draws or converts
// (48 dp at xxhdpi).
const convertedIconSize = 144

// apkIcon returns the icon of an apk as png. iconPath is the icon file in the
// apk as apkparser resolves it (a png if the apk has one). A png icon is
// returned as it is; WebP icons, vector drawables, layer lists and adaptive
// icons are converted. kind says which it was ("png", "webp", "vector", ...).
func apkIcon(apkPath, iconPath string) (data []byte, kind string, err error) {
	if iconPath == "" {
		return nil, "", errors.New("the apk has no icon")
	}
	zr, err := zip.OpenReader(apkPath)
	if err != nil {
		return nil, "", err
	}
	defer zr.Close()
	a := &iconApk{path: apkPath, files: map[string]*zip.File{}}
	for _, f := range zr.File {
		a.files[f.Name] = f
	}

	if strings.EqualFold(path.Ext(iconPath), ".png") {
		b, err := a.read(iconPath)
		return b, "png", err
	}

	img, kind, err := a.render(iconPath, convertedIconSize, 0)
	if err != nil {
		return nil, "", err
	}
	if isTransparent(img) {
		return nil, "", fmt.Errorf("%s (%s) could not be drawn", iconPath, kind)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), kind, nil
}

type iconApk struct {
	path  string
	files map[string]*zip.File
}

func (a *iconApk) read(name string) ([]byte, error) {
	f := a.files[name]
	if f == nil {
		return nil, fmt.Errorf("%s not found in the apk", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 32<<20))
}

// xml returns a binary XML resource of the apk as text, with references
// resolved to file names and values.
func (a *iconApk) xml(name string) (string, error) {
	z, err := apkparser.OpenZip(a.path)
	if err != nil {
		return "", err
	}
	defer z.Close()
	var sb strings.Builder
	p, _ := apkparser.NewParser(z, xml.NewEncoder(&sb))
	if err := p.ParseXml(name); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// bestBitmap returns the largest bitmap with the same name in another density
// folder, e.g. res/mipmap-xxxhdpi-v4/ic_launcher.webp for
// res/mipmap-mdpi-v4/ic_launcher.webp.
func (a *iconApk) bestBitmap(name string) string {
	dir, base := path.Split(name)
	if !strings.HasPrefix(dir, "res/") || strings.Count(dir, "/") != 2 {
		return name
	}
	kind := strings.SplitN(strings.TrimSuffix(dir[len("res/"):], "/"), "-", 2)[0] // mipmap, drawable
	stem := strings.TrimSuffix(base, path.Ext(base))
	best, size := name, uint64(0)
	for n, f := range a.files {
		d, b := path.Split(n)
		if !strings.HasPrefix(d, "res/"+kind) || strings.TrimSuffix(b, path.Ext(b)) != stem {
			continue
		}
		if e := strings.ToLower(path.Ext(b)); e != ".png" && e != ".webp" {
			continue
		}
		if f.UncompressedSize64 > size || (f.UncompressedSize64 == size && n < best) {
			best, size = n, f.UncompressedSize64
		}
	}
	return best
}

func (a *iconApk) render(name string, size, depth int) (image.Image, string, error) {
	if depth > 5 {
		return nil, "", errors.New("drawable nested too deeply")
	}
	if c, ok := parseColor(name); ok {
		img := image.NewNRGBA(image.Rect(0, 0, size, size))
		draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
		return img, "color", nil
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".webp", ".jpg", ".jpeg":
		best := a.bestBitmap(name)
		data, err := a.read(best)
		if err != nil {
			return nil, "", err
		}
		var img image.Image
		kind := strings.TrimPrefix(strings.ToLower(path.Ext(best)), ".")
		if kind == "webp" {
			img, err = webp.Decode(bytes.NewReader(data))
		} else {
			img, _, err = image.Decode(bytes.NewReader(data))
		}
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", best, err)
		}
		return scaleImage(img, size), kind, nil
	case ".xml":
		src, err := a.xml(name)
		if err != nil {
			return nil, "", err
		}
		return a.renderXML(name, src, size, depth)
	}
	return nil, "", fmt.Errorf("%s: unsupported icon format", name)
}

// renderXML draws a drawable XML resource.
func (a *iconApk) renderXML(name, src string, size, depth int) (image.Image, string, error) {
	dec := xml.NewDecoder(strings.NewReader(src))
	var root string
	var elems []xml.StartElement
	for {
		t, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := t.(xml.StartElement); ok {
			if root == "" {
				root = se.Name.Local
			}
			elems = append(elems, se.Copy())
		}
	}
	switch root {
	case "vector":
		img, err := renderVector(src, size)
		return img, "vector", err
	case "layer-list", "adaptive-icon":
		// Adaptive icon layers are 108 dp, of which the launcher shows the
		// middle 72 dp
		full := size
		if root == "adaptive-icon" {
			full = size * 3 / 2
		}
		dst := image.NewNRGBA(image.Rect(0, 0, full, full))
		for _, se := range elems[1:] {
			if se.Name.Local == "monochrome" {
				continue // themed icon layer of Android 13
			}
			d, ok := attr(se, "drawable")
			if !ok {
				continue
			}
			img, _, err := a.render(d, full, depth+1)
			if err != nil {
				return nil, "", err
			}
			draw.Draw(dst, dst.Bounds(), img, image.Point{}, draw.Over)
		}
		if full != size {
			off := (full - size) / 2
			crop := image.NewNRGBA(image.Rect(0, 0, size, size))
			draw.Draw(crop, crop.Bounds(), dst, image.Pt(off, off), draw.Src)
			return crop, root, nil
		}
		return dst, root, nil
	case "bitmap", "inset":
		for _, k := range []string{"src", "drawable"} {
			if d, ok := attr(elems[0], k); ok {
				img, kind, err := a.render(d, size, depth+1)
				return img, root + " " + kind, err
			}
		}
	}
	return nil, "", fmt.Errorf("%s: <%s> icons are not supported", name, root)
}

func scaleImage(img image.Image, size int) image.Image {
	b := img.Bounds()
	if b.Dx() == size && b.Dy() == size {
		return img
	}
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func isTransparent(img image.Image) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, al := img.At(x, y).RGBA(); al != 0 {
				return false
			}
		}
	}
	return true
}
