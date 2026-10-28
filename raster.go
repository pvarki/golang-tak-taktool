package main

// A small rasterizer for vector icons: paths are flattened to polygons and
// filled with 4x4 supersampling, with Android's nonZero or evenOdd rule.
// Strokes are turned into polygons (a quad per segment, round joins, butt,
// round or square caps) and filled with nonZero.

import (
	"image"
	"image/color"
	"math"
	"slices"
)

type pt struct{ x, y float64 }

type subpath struct {
	pts    []pt
	closed bool
}

// flattener collects a path as polygons. It has the methods of a path sink
// that tracePath writes to.
type flattener struct {
	paths []subpath
}

func (f *flattener) MoveTo(x, y float32) {
	f.paths = append(f.paths, subpath{pts: []pt{{float64(x), float64(y)}}})
}

func (f *flattener) cur() *subpath {
	if len(f.paths) == 0 {
		f.paths = append(f.paths, subpath{pts: []pt{{0, 0}}})
	}
	return &f.paths[len(f.paths)-1]
}

func (f *flattener) LineTo(x, y float32) {
	s := f.cur()
	s.pts = append(s.pts, pt{float64(x), float64(y)})
}

func (f *flattener) CubeTo(x1, y1, x2, y2, x, y float32) {
	s := f.cur()
	p0 := s.pts[len(s.pts)-1]
	p1, p2, p3 := pt{float64(x1), float64(y1)}, pt{float64(x2), float64(y2)}, pt{float64(x), float64(y)}
	// Enough segments for the curve's length in pixels
	l := dist(p0, p1) + dist(p1, p2) + dist(p2, p3)
	n := int(math.Min(64, math.Max(4, math.Ceil(l/2))))
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		s.pts = append(s.pts, pt{
			u*u*u*p0.x + 3*u*u*t*p1.x + 3*u*t*t*p2.x + t*t*t*p3.x,
			u*u*u*p0.y + 3*u*u*t*p1.y + 3*u*t*t*p2.y + t*t*t*p3.y,
		})
	}
}

func (f *flattener) ClosePath() {
	if len(f.paths) > 0 {
		s := &f.paths[len(f.paths)-1]
		s.closed = true
		// A path drawn on after Z starts again at the subpath's start
		f.paths = append(f.paths, subpath{pts: []pt{s.pts[0]}})
	}
}

func dist(a, b pt) float64 { return math.Hypot(a.x-b.x, a.y-b.y) }

// polygons returns the subpaths with at least 2 points (for fills, every
// subpath counts as closed).
func (f *flattener) polygons() [][]pt {
	var out [][]pt
	for _, s := range f.paths {
		if len(s.pts) >= 2 {
			out = append(out, s.pts)
		}
	}
	return out
}

// fillPolygons draws the polygons over dst in colour c, with the evenOdd
// rule or else nonZero.
func fillPolygons(dst *image.NRGBA, polys [][]pt, c color.NRGBA, evenOdd bool) {
	type edge struct {
		x0, y0, x1, y1 float64
		dir            int
	}
	var edges []edge
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, poly := range polys {
		for i := range poly {
			a, b := poly[i], poly[(i+1)%len(poly)]
			if a.y == b.y {
				continue
			}
			dir := 1
			if a.y > b.y {
				a, b, dir = b, a, -1
			}
			edges = append(edges, edge{a.x, a.y, b.x, b.y, dir})
			minY, maxY = math.Min(minY, a.y), math.Max(maxY, b.y)
		}
	}
	if len(edges) == 0 {
		return
	}
	w, h := dst.Rect.Dx(), dst.Rect.Dy()
	const ss = 4 // samples per pixel in each direction
	cover := make([]int, w)
	type cross struct {
		x   float64
		dir int
	}
	var xs []cross
	y0 := max(0, int(math.Floor(minY)))
	y1 := min(h, int(math.Ceil(maxY)))
	for py := y0; py < y1; py++ {
		clear(cover)
		any := false
		for sy := 0; sy < ss; sy++ {
			y := float64(py) + (float64(sy)+0.5)/ss
			xs = xs[:0]
			for _, e := range edges {
				if y >= e.y0 && y < e.y1 {
					xs = append(xs, cross{e.x0 + (y-e.y0)*(e.x1-e.x0)/(e.y1-e.y0), e.dir})
				}
			}
			if len(xs) < 2 {
				continue
			}
			slices.SortFunc(xs, func(a, b cross) int {
				switch {
				case a.x < b.x:
					return -1
				case a.x > b.x:
					return 1
				}
				return 0
			})
			wind := 0
			for i := 0; i < len(xs)-1; i++ {
				if evenOdd {
					wind ^= 1
				} else {
					wind += xs[i].dir
				}
				if wind == 0 {
					continue
				}
				// Samples at x = px + (k+0.5)/ss inside [xs[i].x, xs[i+1].x)
				lo := int(math.Ceil(xs[i].x*ss - 0.5))
				hi := int(math.Ceil(xs[i+1].x*ss - 0.5))
				lo, hi = max(lo, 0), min(hi, w*ss)
				for s := lo; s < hi; s++ {
					cover[s/ss]++
					any = true
				}
			}
		}
		if !any {
			continue
		}
		for px := 0; px < w; px++ {
			if cover[px] == 0 {
				continue
			}
			a := float64(c.A) / 255 * float64(cover[px]) / (ss * ss)
			blendOver(dst, px, py, c, a)
		}
	}
}

// blendOver draws colour c with coverage a (0..1) over the pixel.
func blendOver(dst *image.NRGBA, x, y int, c color.NRGBA, a float64) {
	i := dst.PixOffset(x, y)
	p := dst.Pix[i : i+4 : i+4]
	da := float64(p[3]) / 255
	oa := a + da*(1-a)
	if oa <= 0 {
		return
	}
	mix := func(sc, dc uint8) uint8 {
		return uint8(math.Round((float64(sc)*a + float64(dc)*da*(1-a)) / oa))
	}
	p[0], p[1], p[2] = mix(c.R, p[0]), mix(c.G, p[1]), mix(c.B, p[2])
	p[3] = uint8(math.Round(oa * 255))
}

// strokePolygons turns subpaths into polygons that cover a stroke of width w
// (pixels): a quad per segment and a circle at each joint; caps are "butt",
// "round" or "square". All polygons are wound the same way, so that a
// nonZero fill draws their union.
func strokePolygons(paths []subpath, w float64, cap string) [][]pt {
	r := w / 2
	var out [][]pt
	add := func(poly []pt) {
		if area(poly) < 0 {
			slices.Reverse(poly)
		}
		out = append(out, poly)
	}
	circle := func(c pt) []pt {
		n := int(math.Max(8, math.Ceil(r*2)))
		poly := make([]pt, n)
		for i := range poly {
			t := 2 * math.Pi * float64(i) / float64(n)
			poly[i] = pt{c.x + r*math.Cos(t), c.y + r*math.Sin(t)}
		}
		return poly
	}
	for _, s := range paths {
		pts := s.pts
		if s.closed && len(pts) > 1 && pts[0] != pts[len(pts)-1] {
			pts = append(slices.Clone(pts), pts[0])
		}
		if len(pts) < 2 {
			continue
		}
		for i := 0; i+1 < len(pts); i++ {
			a, b := pts[i], pts[i+1]
			l := dist(a, b)
			if l == 0 {
				continue
			}
			dx, dy := (b.x-a.x)/l, (b.y-a.y)/l
			nx, ny := -dy*r, dx*r
			ea, eb := a, b
			if !s.closed && cap == "square" {
				if i == 0 {
					ea = pt{a.x - dx*r, a.y - dy*r}
				}
				if i+2 == len(pts) {
					eb = pt{b.x + dx*r, b.y + dy*r}
				}
			}
			add([]pt{{ea.x + nx, ea.y + ny}, {eb.x + nx, eb.y + ny}, {eb.x - nx, eb.y - ny}, {ea.x - nx, ea.y - ny}})
		}
		// Joints (round), and round caps at the ends of an open path
		for i, p := range pts {
			end := i == 0 || i == len(pts)-1
			if end && !s.closed && cap != "round" {
				continue
			}
			add(circle(p))
		}
	}
	return out
}

func area(poly []pt) float64 {
	s := 0.0
	for i := range poly {
		a, b := poly[i], poly[(i+1)%len(poly)]
		s += a.x*b.y - b.x*a.y
	}
	return s / 2
}
