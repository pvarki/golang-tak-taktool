package main

import (
	"image"
	"image/color"
	"testing"
)

func TestParseColor(t *testing.T) {
	for in, want := range map[string]color.NRGBA{
		"-1":        {255, 255, 255, 255},
		"-16777216": {0, 0, 0, 255},
		"#ff262626": {0x26, 0x26, 0x26, 0xff},
		"#102030":   {0x10, 0x20, 0x30, 0xff},
	} {
		if got, ok := parseColor(in); !ok || got != want {
			t.Errorf("parseColor(%q) = %v, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "res/color/x.xml", "?attr/colorControlNormal"} {
		if _, ok := parseColor(in); ok {
			t.Errorf("parseColor(%q) should fail", in)
		}
	}
}

func alphaAt(img image.Image, x, y int) uint32 {
	_, _, _, a := img.At(x, y).RGBA()
	return a
}

func TestRenderVector(t *testing.T) {
	// A white square in the left half and a circle (arcs) on the right, as
	// apkparser decodes a VectorDrawable
	src := `<vector viewportWidth="20" viewportHeight="10">
		<path fillColor="-1" pathData="M0,0h10v10H0z"></path>
		<group translateX="10"><path fillColor="-16777216" pathData="M5,0a5,5 0,1 1,0 10a5,5 0,1 1,0 -10z"></path></group>
		<path fillColor="0" pathData="M0,0L20,0 20,10z"></path>
	</vector>`
	img, err := renderVector(src, 40)
	if err != nil {
		t.Fatal(err)
	}
	// The 20x10 viewport is centred in 40x40: y from 10 to 30
	if c := img.NRGBAAt(5, 20); c != (color.NRGBA{255, 255, 255, 255}) {
		t.Errorf("square: %v", c)
	}
	if c := img.NRGBAAt(30, 20); c != (color.NRGBA{0, 0, 0, 255}) {
		t.Errorf("circle centre: %v", c)
	}
	if a := alphaAt(img, 21, 11); a != 0 {
		t.Errorf("outside the circle: alpha %d", a)
	}
	if a := alphaAt(img, 5, 5); a != 0 {
		t.Errorf("outside the viewport: alpha %d", a)
	}
}

func TestRenderVectorTint(t *testing.T) {
	img, err := renderVector(`<vector viewportWidth="1" viewportHeight="1" tint="#ff0000ff"><path fillColor="-1" pathData="M0,0h1v1h-1z"></path></vector>`, 4)
	if err != nil {
		t.Fatal(err)
	}
	if c := img.NRGBAAt(2, 2); c != (color.NRGBA{0, 0, 255, 255}) {
		t.Errorf("tinted: %v", c)
	}
}

func TestPathCommands(t *testing.T) {
	// Relative, implicit and smooth commands must parse without errors
	for _, d := range []string{
		"M1,1l2,0 0,2z",
		"m1 1 2 0 0 2Z",
		"M0,0c1,1 2,2 3,3s4,4 5,5q1,1 2,2t3,3",
		"M0,0C1,1,2,2,3,3S4,4,5,5Q1,1,2,2T3,3H5V6h-1v-1",
		"M1e-1,.5L-.5-.5",
		"M0,0a5,5 0,1 1,0 10A5 5 0 1010 10",
	} {
		if _, err := renderVector(`<vector viewportWidth="10" viewportHeight="10"><path fillColor="-1" pathData="`+d+`"></path></vector>`, 10); err != nil {
			t.Errorf("%s: %v", d, err)
		}
	}
	if _, err := renderVector(`<vector viewportWidth="10" viewportHeight="10"><path fillColor="-1" pathData="M0,0X1,1"></path></vector>`, 10); err == nil {
		t.Error("bad path data should fail")
	}
}

func TestRenderLayerList(t *testing.T) {
	a := &iconApk{}
	img, kind, err := a.renderXML("res/r2.xml", `<layer-list><item drawable="#ff262626"></item></layer-list>`, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	if kind != "layer-list" || img.At(4, 4) != (color.NRGBA{0x26, 0x26, 0x26, 0xff}) {
		t.Errorf("%s: %v", kind, img.At(4, 4))
	}
	// The monochrome layer of an adaptive icon is not drawn
	img, _, err = a.renderXML("x.xml", `<adaptive-icon><background drawable="#ff000000"></background><monochrome drawable="#ffffffff"></monochrome></adaptive-icon>`, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _, _ := img.At(4, 4).RGBA(); r != 0 {
		t.Error("monochrome layer was drawn")
	}
}
