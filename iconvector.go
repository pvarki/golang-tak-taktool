package main

import (
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/vector"
)

type mat [6]float64 // a b c d e f: x' = a*x + c*y + e, y' = b*x + d*y + f

func (m mat) mul(n mat) mat { // m then n
	return mat{
		n[0]*m[0] + n[2]*m[1], n[1]*m[0] + n[3]*m[1],
		n[0]*m[2] + n[2]*m[3], n[1]*m[2] + n[3]*m[3],
		n[0]*m[4] + n[2]*m[5] + n[4], n[1]*m[4] + n[3]*m[5] + n[5],
	}
}
func (m mat) apply(x, y float64) (float32, float32) {
	return float32(m[0]*x + m[2]*y + m[4]), float32(m[1]*x + m[3]*y + m[5])
}

func attr(se xml.StartElement, name string) (string, bool) {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}
func fattr(se xml.StartElement, name string, def float64) float64 {
	if v, ok := attr(se, name); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// parseColor parses a colour as apkparser decodes it: a signed decimal ARGB
// value ("-1" is white) or #rrggbb / #aarrggbb.
func parseColor(v string) (color.NRGBA, bool) {
	if v == "" {
		return color.NRGBA{}, false
	}
	if strings.HasPrefix(v, "#") {
		h := v[1:]
		if len(h) == 6 {
			h = "ff" + h
		}
		n, err := strconv.ParseUint(h, 16, 32)
		if err != nil || len(h) != 8 {
			return color.NRGBA{}, false
		}
		return color.NRGBA{uint8(n >> 16), uint8(n >> 8), uint8(n), uint8(n >> 24)}, true
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		u := uint32(n)
		return color.NRGBA{uint8(u >> 16), uint8(u >> 8), uint8(u), uint8(u >> 24)}, true
	}
	return color.NRGBA{}, false
}

// renderVector draws an Android VectorDrawable (as decoded XML) into a
// size x size image. Fills are supported, strokes and clip paths are not.
func renderVector(src string, size int) (*image.NRGBA, error) {
	dec := xml.NewDecoder(strings.NewReader(src))
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	var stack []mat
	var cur mat
	var tint color.NRGBA
	hasTint := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "vector":
				vw, vh := fattr(t, "viewportWidth", 24), fattr(t, "viewportHeight", 24)
				if vw <= 0 || vh <= 0 {
					return nil, fmt.Errorf("bad viewport")
				}
				if tc, ok := attr(t, "tint"); ok {
					tint, hasTint = parseColor(tc)
				}
				s := float64(size) / math.Max(vw, vh)
				cur = mat{s, 0, 0, s, (float64(size) - vw*s) / 2, (float64(size) - vh*s) / 2}
			case "group":
				stack = append(stack, cur)
				px, py := fattr(t, "pivotX", 0), fattr(t, "pivotY", 0)
				sx, sy := fattr(t, "scaleX", 1), fattr(t, "scaleY", 1)
				rot := fattr(t, "rotation", 0) * math.Pi / 180
				tx, ty := fattr(t, "translateX", 0), fattr(t, "translateY", 0)
				// Android: translate(-pivot), scale, rotate, translate(pivot + translate)
				g := mat{1, 0, 0, 1, -px, -py}
				g = g.mul(mat{sx, 0, 0, sy, 0, 0})
				g = g.mul(mat{math.Cos(rot), math.Sin(rot), -math.Sin(rot), math.Cos(rot), 0, 0})
				g = g.mul(mat{1, 0, 0, 1, px + tx, py + ty})
				cur = g.mul(cur)
			case "path":
				d, _ := attr(t, "pathData")
				fc, _ := attr(t, "fillColor")
				c, ok := parseColor(fc)
				if !ok || c.A == 0 {
					continue
				}
				c.A = uint8(float64(c.A) * fattr(t, "fillAlpha", 1))
				r := vector.NewRasterizer(size, size)
				if err := tracePath(r, d, cur); err != nil {
					return nil, err
				}
				r.Draw(dst, dst.Bounds(), image.NewUniform(c), image.Point{})
			}
		case xml.EndElement:
			if t.Name.Local == "group" && len(stack) > 0 {
				cur, stack = stack[len(stack)-1], stack[:len(stack)-1]
			}
		}
	}
	if hasTint {
		// android:tint replaces the colour and keeps the alpha (SRC_IN)
		for i := 0; i < len(dst.Pix); i += 4 {
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2] = tint.R, tint.G, tint.B
			dst.Pix[i+3] = uint8(int(dst.Pix[i+3]) * int(tint.A) / 255)
		}
	}
	return dst, nil
}

// tracePath feeds SVG path data to the rasterizer.
func tracePath(r *vector.Rasterizer, d string, m mat) error {
	p := pathLexer{s: d}
	var cx, cy, sx, sy, lcx, lcy float64 // current, subpath start, last control
	var lastCmd byte
	open := false
	move := func(x, y float64) {
		if open {
			r.ClosePath()
		}
		r.MoveTo(m.apply(x, y))
		open = true
	}
	line := func(x, y float64) { r.LineTo(m.apply(x, y)) }
	cube := func(x1, y1, x2, y2, x, y float64) {
		a, b := m.apply(x1, y1)
		c, dd := m.apply(x2, y2)
		e, f := m.apply(x, y)
		r.CubeTo(a, b, c, dd, e, f)
	}
	var cmd byte
	for {
		if c, ok := p.cmd(); ok {
			cmd = c
		} else if p.done() {
			break
		} else if cmd == 0 {
			return fmt.Errorf("bad path data at %d", p.i)
		} else if cmd == 'M' {
			cmd = 'L'
		} else if cmd == 'm' {
			cmd = 'l'
		}
		rel := cmd >= 'a'
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = cx, cy
		}
		switch cmd | 0x20 {
		case 'z':
			if open {
				r.ClosePath()
				open = false
			}
			cx, cy = sx, sy
			lastCmd = 'z'
			cmd = 0
			continue
		case 'm':
			x, y, err := p.nums2()
			if err != nil {
				return err
			}
			cx, cy = ox+x, oy+y
			sx, sy = cx, cy
			move(cx, cy)
		case 'l':
			x, y, err := p.nums2()
			if err != nil {
				return err
			}
			if !open {
				move(cx, cy)
			}
			cx, cy = ox+x, oy+y
			line(cx, cy)
		case 'h':
			x, err := p.num()
			if err != nil {
				return err
			}
			if !open {
				move(cx, cy)
			}
			cx = ox + x
			line(cx, cy)
		case 'v':
			y, err := p.num()
			if err != nil {
				return err
			}
			if !open {
				move(cx, cy)
			}
			cy = oy + y
			line(cx, cy)
		case 'c', 's', 'q', 't':
			var x1, y1, x2, y2, x, y float64
			var err error
			lc := lastCmd | 0x20
			switch cmd | 0x20 {
			case 'c':
				x1, y1, _ = p.nums2()
				x2, y2, _ = p.nums2()
				x, y, err = p.nums2()
				x1, y1, x2, y2 = ox+x1, oy+y1, ox+x2, oy+y2
			case 's':
				x1, y1 = cx, cy
				if lc == 'c' || lc == 's' {
					x1, y1 = 2*cx-lcx, 2*cy-lcy
				}
				x2, y2, _ = p.nums2()
				x, y, err = p.nums2()
				x2, y2 = ox+x2, oy+y2
			case 'q', 't':
				var qx, qy float64
				if cmd|0x20 == 'q' {
					qx, qy, _ = p.nums2()
					qx, qy = ox+qx, oy+qy
				} else {
					qx, qy = cx, cy
					if lc == 'q' || lc == 't' {
						qx, qy = 2*cx-lcx, 2*cy-lcy
					}
				}
				x, y, err = p.nums2()
				ex, ey := ox+x, oy+y
				x1, y1 = cx+2.0/3*(qx-cx), cy+2.0/3*(qy-cy)
				x2, y2 = ex+2.0/3*(qx-ex), ey+2.0/3*(qy-ey)
				lcx, lcy = qx, qy
				if !open {
					move(cx, cy)
				}
				cube(x1, y1, x2, y2, ex, ey)
				cx, cy = ex, ey
				lastCmd = cmd
				if err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if !open {
				move(cx, cy)
			}
			x, y = ox+x, oy+y
			cube(x1, y1, x2, y2, x, y)
			lcx, lcy = x2, y2
			cx, cy = x, y
		case 'a':
			rx, _ := p.num()
			ry, _ := p.num()
			phi, _ := p.num()
			large, _ := p.flag()
			sweep, _ := p.flag()
			x, y, err := p.nums2()
			if err != nil {
				return err
			}
			if !open {
				move(cx, cy)
			}
			x, y = ox+x, oy+y
			arcToCubics(cx, cy, rx, ry, phi, large, sweep, x, y, cube, line)
			cx, cy = x, y
		default:
			return fmt.Errorf("unknown path command %q", cmd)
		}
		lastCmd = cmd
	}
	if open {
		r.ClosePath()
	}
	return nil
}

func arcToCubics(x1, y1, rx, ry, phiDeg float64, large, sweep bool, x2, y2 float64,
	cube func(a, b, c, d, e, f float64), line func(x, y float64)) {
	if rx == 0 || ry == 0 || (x1 == x2 && y1 == y2) {
		line(x2, y2)
		return
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	phi := phiDeg * math.Pi / 180
	cp, sp := math.Cos(phi), math.Sin(phi)
	dx, dy := (x1-x2)/2, (y1-y2)/2
	x1p, y1p := cp*dx+sp*dy, -sp*dx+cp*dy
	lam := x1p*x1p/(rx*rx) + y1p*y1p/(ry*ry)
	if lam > 1 {
		s := math.Sqrt(lam)
		rx, ry = rx*s, ry*s
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	co := 0.0
	if den != 0 && num > 0 {
		co = math.Sqrt(num / den)
	}
	if large == sweep {
		co = -co
	}
	cxp, cyp := co*rx*y1p/ry, -co*ry*x1p/rx
	cx := cp*cxp - sp*cyp + (x1+x2)/2
	cy := sp*cxp + cp*cyp + (y1+y2)/2
	ang := func(ux, uy, vx, vy float64) float64 {
		a := math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
		return a
	}
	t1 := ang(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	dt := ang((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	} else if sweep && dt < 0 {
		dt += 2 * math.Pi
	}
	n := int(math.Ceil(math.Abs(dt) / (math.Pi / 2)))
	seg := dt / float64(n)
	k := 4.0 / 3 * math.Tan(seg/4)
	pt := func(t float64) (float64, float64) {
		return cx + rx*math.Cos(t)*cp - ry*math.Sin(t)*sp, cy + rx*math.Cos(t)*sp + ry*math.Sin(t)*cp
	}
	der := func(t float64) (float64, float64) {
		return -rx*math.Sin(t)*cp - ry*math.Cos(t)*sp, -rx*math.Sin(t)*sp + ry*math.Cos(t)*cp
	}
	t := t1
	for i := 0; i < n; i++ {
		ax, ay := pt(t)
		bx, by := pt(t + seg)
		dax, day := der(t)
		dbx, dby := der(t + seg)
		cube(ax+k*dax, ay+k*day, bx-k*dbx, by-k*dby, bx, by)
		t += seg
	}
}

type pathLexer struct {
	s string
	i int
}

func (p *pathLexer) skip() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == ',' || p.s[p.i] == '\n' || p.s[p.i] == '\t' || p.s[p.i] == '\r') {
		p.i++
	}
}
func (p *pathLexer) done() bool { p.skip(); return p.i >= len(p.s) }
func (p *pathLexer) cmd() (byte, bool) {
	p.skip()
	if p.i < len(p.s) && strings.IndexByte("MmLlHhVvCcSsQqTtAaZz", p.s[p.i]) >= 0 {
		p.i++
		return p.s[p.i-1], true
	}
	return 0, false
}
func (p *pathLexer) flag() (bool, error) {
	p.skip()
	if p.i < len(p.s) && (p.s[p.i] == '0' || p.s[p.i] == '1') {
		p.i++
		return p.s[p.i-1] == '1', nil
	}
	return false, fmt.Errorf("bad flag at %d", p.i)
}
func (p *pathLexer) num() (float64, error) {
	p.skip()
	st := p.i
	if p.i < len(p.s) && (p.s[p.i] == '-' || p.s[p.i] == '+') {
		p.i++
	}
	dot, exp := false, false
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c >= '0' && c <= '9':
		case c == '.' && !dot && !exp:
			dot = true
		case (c == 'e' || c == 'E') && !exp:
			exp = true
			if p.i+1 < len(p.s) && (p.s[p.i+1] == '-' || p.s[p.i+1] == '+') {
				p.i++
			}
		default:
			goto end
		}
		p.i++
	}
end:
	if st == p.i {
		return 0, fmt.Errorf("number expected at %d", p.i)
	}
	return strconv.ParseFloat(p.s[st:p.i], 64)
}
func (p *pathLexer) nums2() (float64, float64, error) {
	x, err := p.num()
	if err != nil {
		return 0, 0, err
	}
	y, err := p.num()
	return x, y, err
}
