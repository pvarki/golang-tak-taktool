package main

import (
	"image"
	"image/color"
	"slices"
	"strconv"
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
	// 16843817 = 0x01010433, Android's colorControlNormal attribute;
	// 2130903040 = 0x7f030000, an attribute of the app
	for _, in := range []string{"", "res/color/x.xml", "?attr/colorControlNormal", "16843817", "2130903040"} {
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

func TestComplexValue(t *testing.T) {
	// 0x3001 is 48dp (the width of a vector icon), 0x1800 is 24 without unit
	if v, u := complexValue(0x3001); v != 48 || u != 1 {
		t.Errorf("48dp: %v %v", v, u)
	}
	if v, u := complexValue(0x1800); v != 24 || u != 0 {
		t.Errorf("24: %v %v", v, u)
	}
}

func TestRenderInsetAndAlpha(t *testing.T) {
	a := &iconApk{}
	// An inset of 25% around a black square, inside a layer list
	// 0.25 as a complex fraction: value 0.25 = int32(c&0xffffff00) / 2^31 with radix 3
	c := uint32(0.25*float64(1<<31))&0xffffff00 | 3<<4
	src := `<layer-list><item><inset inset="` + itoa(c) + `"><color color="#ff000000"></color></inset></item></layer-list>`
	img, kind, err := a.renderXML("x.xml", src, 40, 0)
	if err != nil {
		t.Fatal(err)
	}
	if alphaAt(img, 5, 5) != 0 || alphaAt(img, 20, 20) == 0 {
		t.Errorf("%s: inset not applied: corner %d, centre %d", kind, alphaAt(img, 5, 5), alphaAt(img, 20, 20))
	}

	v, err := renderVector(`<vector viewportWidth="1" viewportHeight="1" alpha="0.5"><path fillColor="-1" pathData="M0,0h1v1h-1z"></path></vector>`, 4)
	if err != nil {
		t.Fatal(err)
	}
	if c := v.NRGBAAt(2, 2); c.A < 120 || c.A > 135 {
		t.Errorf("alpha: %v", c)
	}
}

func itoa(c uint32) string { return strconv.FormatInt(int64(int32(c)), 10) }

func TestRenderVectorNotes(t *testing.T) {
	src := `<vector viewportWidth="10" viewportHeight="10">
		<clip-path pathData="M0,0h10v10h-10z"></clip-path>
		<path fillColor="-1" fillType="1" pathData="M0,0h10v10h-10z"></path>
		<path strokeColor="-16777216" strokeWidth="1" pathData="M0,0L10,10"></path>
		<path fillColor="res/color/gradient.xml" pathData="M0,0h5v5h-5z"></path>
	</vector>`
	_, notes, err := renderVectorNotes(src, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"clip paths", "gradients or colours that could not be resolved"}; !slices.Equal(notes, want) {
		t.Errorf("notes %v, want %v", notes, want)
	}
	if _, notes, _ := renderVectorNotes(`<vector viewportWidth="1" viewportHeight="1"><path fillColor="-1" pathData="M0,0h1v1h-1z"></path></vector>`, 4); len(notes) != 0 {
		t.Errorf("plain icon has notes %v", notes)
	}
}

func TestRenderVectorEvenOddAndStroke(t *testing.T) {
	// A square with a square hole: evenOdd leaves the hole empty, nonZero
	// (both squares wound the same way) fills it
	ring := "M0,0h10v10h-10z M3,3h4v4h-4z"
	for rule, holeFilled := range map[string]bool{"1": false, "0": true} {
		img, err := renderVector(`<vector viewportWidth="10" viewportHeight="10"><path fillColor="-1" fillType="`+rule+`" pathData="`+ring+`"></path></vector>`, 20)
		if err != nil {
			t.Fatal(err)
		}
		if got := alphaAt(img, 10, 10) > 0; got != holeFilled {
			t.Errorf("fillType %s: hole filled %v", rule, got)
		}
		if alphaAt(img, 2, 2) == 0 {
			t.Errorf("fillType %s: ring not drawn", rule)
		}
	}

	// An open stroke from left to right, 2 units wide: drawn along the line,
	// not elsewhere, and not closed back to the start
	img, err := renderVector(`<vector viewportWidth="10" viewportHeight="10"><path strokeColor="-16777216" strokeWidth="2" pathData="M1,5L9,5L9,9"></path></vector>`, 20)
	if err != nil {
		t.Fatal(err)
	}
	if c := img.NRGBAAt(10, 10); c.A < 250 || c.R != 0 {
		t.Errorf("on the line: %v", c)
	}
	if alphaAt(img, 10, 4) != 0 {
		t.Error("stroke too wide")
	}
	if alphaAt(img, 8, 14) != 0 {
		t.Error("open stroke was closed")
	}
	// Round caps reach past the end, butt caps do not
	for capv, past := range map[string]bool{"1": true, "0": false} {
		img, _ := renderVector(`<vector viewportWidth="10" viewportHeight="10"><path strokeColor="-1" strokeWidth="2" strokeLineCap="`+capv+`" pathData="M3,5L7,5"></path></vector>`, 20)
		if got := alphaAt(img, 5, 10) > 0; got != past {
			t.Errorf("cap %s: past the end %v", capv, got)
		}
	}
}
