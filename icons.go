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
	"math"
	"path"
	"slices"
	"strconv"
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
func apkIcon(apkPath, iconPath string) (data []byte, kind string, notes []string, err error) {
	if iconPath == "" {
		return nil, "", nil, errors.New("the apk has no icon")
	}
	zr, err := zip.OpenReader(apkPath)
	if err != nil {
		return nil, "", nil, err
	}
	defer zr.Close()
	a := &iconApk{path: apkPath, files: map[string]*zip.File{}}
	for _, f := range zr.File {
		a.files[f.Name] = f
	}

	if strings.EqualFold(path.Ext(iconPath), ".png") {
		b, err := a.read(iconPath)
		return b, "png", nil, err
	}

	img, kind, err := a.render(iconPath, convertedIconSize, 0)
	if err != nil {
		return nil, "", nil, err
	}
	if nearlyTransparent(img) {
		return nil, "", a.notes, fmt.Errorf("%s (%s) could not be drawn", iconPath, kind)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, "", nil, err
	}
	return buf.Bytes(), kind, a.notes, nil
}

type iconApk struct {
	path  string
	files map[string]*zip.File
	notes []string // what could not be drawn
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

// xnode is an element of a drawable XML resource.
type xnode struct {
	start    xml.StartElement
	children []*xnode
}

func parseXNode(src string) (*xnode, error) {
	dec := xml.NewDecoder(strings.NewReader(src))
	var root *xnode
	var stack []*xnode
	for {
		t, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := t.(type) {
		case xml.StartElement:
			n := &xnode{start: t.Copy()}
			if len(stack) == 0 {
				if root == nil {
					root = n
				}
			} else {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if root == nil {
		return nil, errors.New("empty drawable")
	}
	return root, nil
}

// xml returns the element as XML text again (for an inline <vector>).
func (n *xnode) xml() string {
	var sb strings.Builder
	enc := xml.NewEncoder(&sb)
	var write func(*xnode)
	write = func(n *xnode) {
		enc.EncodeToken(n.start)
		for _, c := range n.children {
			write(c)
		}
		enc.EncodeToken(n.start.End())
	}
	write(n)
	enc.Flush()
	return sb.String()
}

func (n *xnode) attr(name string) (string, bool) { return attr(n.start, name) }

// renderXML draws a drawable XML resource.
func (a *iconApk) renderXML(name, src string, size, depth int) (image.Image, string, error) {
	n, err := parseXNode(src)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", name, err)
	}
	return a.renderNode(name, n, size, depth)
}

// renderNode draws a drawable element: <vector>, <layer-list>,
// <adaptive-icon>, <bitmap>, <inset> or <color>.
func (a *iconApk) renderNode(name string, n *xnode, size, depth int) (image.Image, string, error) {
	if depth > 8 {
		return nil, "", errors.New("drawable nested too deeply")
	}
	root := n.start.Name.Local
	switch root {
	case "vector":
		img, notes, err := renderVectorNotes(n.xml(), size)
		for _, note := range notes {
			if !slices.Contains(a.notes, note) {
				a.notes = append(a.notes, note)
			}
		}
		return img, "vector", err
	case "layer-list", "adaptive-icon":
		// Adaptive icon layers are 108 dp, of which the launcher shows the
		// middle 72 dp
		full := size
		if root == "adaptive-icon" {
			full = size * 3 / 2
		}
		dst := image.NewNRGBA(image.Rect(0, 0, full, full))
		for _, c := range n.children {
			if c.start.Name.Local == "monochrome" {
				continue // themed icon layer of Android 13
			}
			img, _, err := a.renderChild(name, c, full, depth+1)
			if err != nil {
				return nil, "", err
			}
			if img != nil {
				draw.Draw(dst, dst.Bounds(), img, image.Point{}, draw.Over)
			}
		}
		if full != size {
			off := (full - size) / 2
			crop := image.NewNRGBA(image.Rect(0, 0, size, size))
			draw.Draw(crop, crop.Bounds(), dst, image.Pt(off, off), draw.Src)
			return crop, root, nil
		}
		return dst, root, nil
	case "bitmap":
		if d, ok := n.attr("src"); ok {
			img, kind, err := a.render(d, size, depth+1)
			return img, "bitmap " + kind, err
		}
	case "inset":
		l, t, r, b := insets(n, size)
		inner := size - l - r
		if h := size - t - b; h < inner {
			inner = h
		}
		if inner <= 0 {
			return nil, "", fmt.Errorf("%s: insets fill the whole icon", name)
		}
		img, kind, err := a.renderChild(name, n, inner, depth+1)
		if err != nil || img == nil {
			if err == nil {
				err = fmt.Errorf("%s: empty <inset>", name)
			}
			return nil, "", err
		}
		dst := image.NewNRGBA(image.Rect(0, 0, size, size))
		draw.Draw(dst, image.Rect(l, t, l+inner, t+inner), img, image.Point{}, draw.Over)
		return dst, "inset " + kind, nil
	case "color":
		if v, ok := n.attr("color"); ok {
			return a.render(v, size, depth+1)
		}
	}
	return nil, "", fmt.Errorf("%s: <%s> icons are not supported", name, root)
}

// renderChild draws the drawable of an <item>, <background>, <foreground> or
// <inset>: its android:drawable, or the drawable element inside it. It
// returns nil for an element without a drawable.
func (a *iconApk) renderChild(name string, n *xnode, size, depth int) (image.Image, string, error) {
	if d, ok := n.attr("drawable"); ok {
		return a.render(d, size, depth)
	}
	for _, c := range n.children {
		return a.renderNode(name, c, size, depth)
	}
	return nil, "", nil
}

// insets returns the insets of an <inset> in pixels of an icon of size.
func insets(n *xnode, size int) (l, t, r, b int) {
	px := func(names ...string) int {
		for _, name := range names {
			if v, ok := n.attr(name); ok {
				return insetPixels(v, size)
			}
		}
		return 0
	}
	return px("insetLeft", "inset"), px("insetTop", "inset"), px("insetRight", "inset"), px("insetBottom", "inset")
}

// insetPixels converts an inset value as apkparser writes it (a complex
// dimension or fraction as a decimal number) to pixels of an icon of size.
// Fractions (16.7%) are relative to the size; dp values to the 108 dp of an
// adaptive icon layer, the usual place of insets in icons.
func insetPixels(v string, size int) int {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	value, unit := complexValue(uint32(n))
	var frac float64
	switch unit {
	case 0: // a fraction (px insets are not used in icons)
		frac = value
	default: // dp and other dimensions
		frac = value / 108
	}
	if frac < 0 || frac >= 0.5 {
		return 0
	}
	return int(math.Round(frac * float64(size)))
}

// complexValue decodes an Android complex value (dimension or fraction):
// its value and its unit (the lowest 4 bits).
func complexValue(c uint32) (float64, uint32) {
	mult := [4]float64{1.0 / (1 << 8), 1.0 / (1 << 15), 1.0 / (1 << 23), 1.0 / (1 << 31)}
	return float64(int32(c&0xffffff00)) * mult[(c>>4)&3], c & 0xf
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

// nearlyTransparent reports whether no pixel of img is clearly visible (an
// icon drawn with colours that could not be resolved).
func nearlyTransparent(img image.Image) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, al := img.At(x, y).RGBA(); al > 0x1000 {
				return false
			}
		}
	}
	return true
}
