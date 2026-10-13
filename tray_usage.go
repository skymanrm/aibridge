package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"slices"
	"time"

	"github.com/skymanrm/aibridge/bridge"
)

// trayMeter is what the tray icon draws: one bar per AI tool, plus menu lines describing them.
type trayMeter struct {
	Vertical bool
	Icon     bool      // draw the app symbol before the bars
	Dots     bool      // show which AI tools are running requests (their mark, or their bar, pulses)
	Mono     bool      // one color, no level or tool tints
	Tools    bool      // draw a tiny mark of each AI tool by its bar
	Percents []float64 // 0–100, in provider order
	IDs      []string  // provider id per bar
	Resets   []string  // per bar, e.g. "2h"; nil unless enabled
	Active   []int     // running requests per bar
	Other    int       // running requests not shown on a bar
	Lines    []string
}

var trayWindowNames = map[string]string{"session": "5-hour", "week": "weekly"}

// buildTrayMeter picks the configured window of each visible provider that reports it; running counts requests per provider.
func buildTrayMeter(cfg bridge.TrayUsage, limits []bridge.UsageLimits, running map[string]int, now time.Time) trayMeter {
	m := trayMeter{Vertical: cfg.Style == "vertical", Icon: !cfg.HideIcon, Dots: !cfg.HideActivity, Mono: cfg.Monochrome,
		Tools: !cfg.HideToolIcons}
	shown := map[string]bool{}
	for _, u := range limits {
		if slices.Contains(cfg.Hidden, u.Provider) {
			continue
		}
		i := slices.IndexFunc(u.Windows, func(w bridge.UsageWindow) bool { return w.ID == cfg.Window })
		if i < 0 {
			continue
		}
		w := u.Windows[i]
		pct := math.Max(0, math.Min(100, w.UsedPercent))
		if cfg.Style != "off" {
			m.Percents = append(m.Percents, pct)
			m.IDs = append(m.IDs, u.Provider)
			m.Active = append(m.Active, running[u.Provider])
			shown[u.Provider] = true
			if cfg.ShowReset {
				label := ""
				if w.ResetsAt != nil {
					label = resetIn(w.ResetsAt.Sub(now))
				}
				m.Resets = append(m.Resets, label)
			}
		}
		line := fmt.Sprintf("%s: %.0f%% of %s limit", u.Name, w.UsedPercent, trayWindowNames[cfg.Window])
		if w.ResetsAt != nil {
			line += ", resets " + resetClock(*w.ResetsAt, now)
		}
		if n := running[u.Provider]; n > 0 {
			line += fmt.Sprintf(", %d running", n)
		}
		m.Lines = append(m.Lines, line)
	}
	for id, n := range running {
		if !m.Dots || !shown[id] {
			m.Other += n
		}
	}
	if len(m.Percents) == 0 {
		m.Icon = true // never leave the menu bar item empty
	}
	return m
}

// resetIn is a compact time to reset: "45m", "2h" (rounded) or "3d".
func resetIn(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "0m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(math.Ceil(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", max(1, int(math.Round(d.Hours()))))
	}
	return fmt.Sprintf("%dd", int(math.Round(d.Hours()/24)))
}

// resetClock is "15:50" today and "Wed 06:00" on other days.
func resetClock(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("Mon 15:04")
}

// Bar colors match the window: amber from 75%, red from 90%.
func meterLevel(pct float64) int {
	switch {
	case pct >= 90:
		return 2
	case pct >= 75:
		return 1
	}
	return 0
}

var (
	meterTile  = color.NRGBA{0x1b, 0x24, 0x30, 0xff}
	meterTrack = color.NRGBA{0xff, 0xff, 0xff, 0x40}
	meterFills = []color.NRGBA{{0x2f, 0xbf, 0x84, 0xff}, {0xe0, 0xa3, 0x3a, 0xff}, {0xe5, 0x65, 0x5e, 0xff}}
	meterMono  = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	toolTints  = map[string]color.NRGBA{"claude": {0xd9, 0x77, 0x57, 0xff}, "gemini": {0x5b, 0x8c, 0xfa, 0xff}}
)

// meterIconICO draws the bars on a dark rounded tile as a PNG-compressed .ico for the Windows tray.
// Tool marks sit before each bar (above, when vertical) and light up while that tool runs a request.
func meterIconICO(m trayMeter, size int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	fillRound(img, 0, 0, float64(size), float64(size), float64(size)/5, meterTile)
	n := float64(len(m.Percents))
	pad := float64(size) / 7
	inner := float64(size) - 2*pad
	gap := math.Max(1, inner/12)
	thick := math.Min((inner-gap*(n-1))/n, inner/2.5)
	offset := pad + (inner-(thick*n+gap*(n-1)))/2
	mark := 0.0
	if m.Tools {
		mark = math.Min(thick+gap, inner/3)
	}
	start, length := pad, inner
	if mark > 0 {
		start, length = pad+mark+gap, inner-mark-gap
	}
	for i, pct := range m.Percents {
		at := offset + float64(i)*(thick+gap)
		fill := meterFills[meterLevel(pct)]
		if m.Mono {
			fill = meterMono
		}
		if m.Vertical {
			fillRound(img, at, start, thick, length, thick/2, meterTrack)
			fillBar(img, at, start, thick, length, pct, true, fill)
		} else {
			fillRound(img, start, at, length, thick, thick/2, meterTrack)
			fillBar(img, start, at, length, thick, pct, false, fill)
		}
		if mark > 0 && i < len(m.IDs) {
			tint, ok := toolTints[m.IDs[i]]
			if !ok || m.Mono {
				tint = meterMono
			}
			if !(m.Dots && i < len(m.Active) && m.Active[i] > 0) {
				tint.A = 0x8c // idle marks are dimmed
			}
			cx, cy := pad+mark/2, at+thick/2
			if m.Vertical {
				cx, cy = at+thick/2, pad+mark/2
			}
			fillShape(img, cx, cy, mark/2, toolShape(m.IDs[i], mark/2), tint)
		}
	}
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, img)
	return pngICO(pngBuf.Bytes(), size)
}

// toolShape is a tool's tiny mark as an inside test around (0,0) with radius s:
// Claude a starburst, Codex a hexagon ring, Gemini a four-point star, others a dot.
func toolShape(id string, s float64) func(x, y float64) bool {
	switch id {
	case "claude":
		return func(x, y float64) bool {
			for k := range 4 {
				a := float64(k) * math.Pi / 4
				if math.Abs(x*math.Sin(a)-y*math.Cos(a)) <= s*0.19 && math.Abs(x*math.Cos(a)+y*math.Sin(a)) <= s*0.95 {
					return true
				}
			}
			return false
		}
	case "codex":
		hex := func(x, y, r float64) bool {
			for k := range 3 {
				a := float64(k) * math.Pi / 3
				if math.Abs(x*math.Cos(a)+y*math.Sin(a)) > r*math.Cos(math.Pi/6) {
					return false
				}
			}
			return true
		}
		return func(x, y float64) bool { return hex(x, y, s*0.95) && !hex(x, y, s*0.5) }
	case "gemini":
		return func(x, y float64) bool { return math.Sqrt(math.Abs(x))+math.Sqrt(math.Abs(y)) <= math.Sqrt(s) }
	}
	return func(x, y float64) bool { return x*x+y*y <= s*s*0.5 }
}

// fillShape blends the shape centered at (cx, cy) into img with 4x4 supersampled edges.
func fillShape(img *image.NRGBA, cx, cy, s float64, inside func(x, y float64) bool, c color.NRGBA) {
	b := img.Bounds()
	for py := max(b.Min.Y, int(cy-s)); py < min(b.Max.Y, int(math.Ceil(cy+s))); py++ {
		for px := max(b.Min.X, int(cx-s)); px < min(b.Max.X, int(math.Ceil(cx+s))); px++ {
			hits := 0
			for sy := range 4 {
				for sx := range 4 {
					if inside(float64(px)+(float64(sx)+0.5)/4-cx, float64(py)+(float64(sy)+0.5)/4-cy) {
						hits++
					}
				}
			}
			if hits > 0 {
				blend(img, px, py, c, float64(hits)/16)
			}
		}
	}
}

// fillBar fills pct of the pill-shaped track (from the bottom when vertical), clipped to its rounded ends.
func fillBar(img *image.NRGBA, x, y, w, h, pct float64, vertical bool, c color.NRGBA) {
	pill := roundRect(x, y, w, h, math.Min(w, h)/2)
	inside := func(px, py float64) bool { return pill(px, py) && px <= x+w*pct/100 }
	if vertical {
		inside = func(px, py float64) bool { return pill(px, py) && py >= y+h*(1-pct/100) }
	}
	fillArea(img, x, y, w, h, inside, c)
}

// roundRect is the inside test of a rounded rectangle.
func roundRect(x, y, w, h, r float64) func(px, py float64) bool {
	return func(px, py float64) bool {
		cx := math.Max(x+r, math.Min(px, x+w-r))
		cy := math.Max(y+r, math.Min(py, y+h-r))
		return px >= x && px <= x+w && py >= y && py <= y+h && (px-cx)*(px-cx)+(py-cy)*(py-cy) <= r*r
	}
}

// fillRound blends a rounded rectangle into img with 4x4 supersampled edges.
func fillRound(img *image.NRGBA, x, y, w, h, r float64, c color.NRGBA) {
	if w <= 0 || h <= 0 {
		return
	}
	fillArea(img, x, y, w, h, roundRect(x, y, w, h, math.Min(r, math.Min(w, h)/2)), c)
}

// fillArea blends the pixels of the box where inside holds, with 4x4 supersampled edges.
func fillArea(img *image.NRGBA, x, y, w, h float64, inside func(px, py float64) bool, c color.NRGBA) {
	b := img.Bounds()
	for py := max(b.Min.Y, int(y)); py < min(b.Max.Y, int(math.Ceil(y+h))); py++ {
		for px := max(b.Min.X, int(x)); px < min(b.Max.X, int(math.Ceil(x+w))); px++ {
			hits := 0
			for sy := range 4 {
				for sx := range 4 {
					if inside(float64(px)+(float64(sx)+0.5)/4, float64(py)+(float64(sy)+0.5)/4) {
						hits++
					}
				}
			}
			if hits > 0 {
				blend(img, px, py, c, float64(hits)/16)
			}
		}
	}
}

// blend draws c over the pixel with the given coverage (source-over, non-premultiplied).
func blend(img *image.NRGBA, x, y int, c color.NRGBA, coverage float64) {
	dst := img.NRGBAAt(x, y)
	sa := float64(c.A) / 255 * coverage
	da := float64(dst.A) / 255
	oa := sa + da*(1-sa)
	if oa == 0 {
		return
	}
	mix := func(s, d uint8) uint8 {
		return uint8(math.Round((float64(s)*sa + float64(d)*da*(1-sa)) / oa))
	}
	img.SetNRGBA(x, y, color.NRGBA{mix(c.R, dst.R), mix(c.G, dst.G), mix(c.B, dst.B), uint8(math.Round(oa * 255))})
}

// pngICO wraps one PNG image in an .ico container (supported by Windows Vista and later).
func pngICO(data []byte, size int) []byte {
	var b bytes.Buffer
	dim := byte(size)
	if size >= 256 {
		dim = 0
	}
	_ = binary.Write(&b, binary.LittleEndian, []uint16{0, 1, 1})
	b.Write([]byte{dim, dim, 0, 0})
	_ = binary.Write(&b, binary.LittleEndian, []uint16{1, 32})
	_ = binary.Write(&b, binary.LittleEndian, []uint32{uint32(len(data)), 22})
	b.Write(data)
	return b.Bytes()
}
