// Package wmfrender converts Windows Metafile (WMF) binary data to SVG.
// It supports standard WMF (type 1/2) without placeable header.
// EMF files are detected and returned as-is (unsupported).
package wmfrender

import (
	"encoding/binary"
	"fmt"
	"image/color"
	"math"
	"strings"
)

// ─── WMF record function codes ────────────────────────────────────────────────
const (
	wmfEOF               = 0x0000
	wmfRestoreDC         = 0x001C
	wmfSaveDC            = 0x001E
	wmfRealizePalette    = 0x0035
	wmfSetPalEntries     = 0x0037
	wmfSetBkMode         = 0x0102
	wmfSetMapMode        = 0x0103
	wmfSetROP2           = 0x0104
	wmfSetRelAbs         = 0x0105
	wmfSetPolyFillMode   = 0x0106
	wmfSetStretchBltMode = 0x0107
	wmfDeleteObject      = 0x01F0
	wmfScaleViewportExt  = 0x012D
	wmfOffsetWindowOrg   = 0x012E
	wmfSetBkColor        = 0x0201
	wmfSetTextColor      = 0x0209
	wmfSetWindowOrg      = 0x020B
	wmfSetWindowExt      = 0x020C
	wmfSetViewportOrg    = 0x020D
	wmfSetViewportExt    = 0x020E
	wmfOffsetViewportOrg = 0x0211
	wmfMoveTo            = 0x0214
	wmfLineTo            = 0x0213
	wmfSelectObject      = 0x0235
	wmfSetTextAlign      = 0x0236
	wmfSetTextCharExtra  = 0x0108
	wmfCreateFontIndirect = 0x02FA
	wmfCreatePenIndirect  = 0x02FB
	wmfCreatePaletteIndirect = 0x02FC
	wmfCreateBrushIndirect   = 0x02FD
	wmfPolygon           = 0x0324
	wmfPolyLine          = 0x0325
	wmfRectangle         = 0x041B
	wmfPolyPolygon       = 0x0538
	wmfEscape            = 0x0626
	wmfTextOut           = 0x0521
	wmfExtTextOut        = 0x0A32
	wmfScaleWindowExt    = 0x0400
	wmfSelectPalette     = 0x0234
	wmfAnimatePalette    = 0x0436
)

// wmfObject represents a GDI object in the WMF object table.
type wmfObject struct {
	kind  int // 1=pen, 2=brush, 3=font, 4=palette
	pen   penObj
	brush brushObj
	pal   paletteObj
}

type penObj struct {
	style uint16
	width int16
	col   color.RGBA
}

type brushObj struct {
	style uint16
	col   color.RGBA
}

type paletteObj struct {
	entries []color.RGBA
}

// dc is the "device context" that tracks WMF state.
type dc struct {
	// Window/Viewport
	windowOrgX, windowOrgY   float64
	windowExtX, windowExtY   float64
	viewportOrgX, viewportOrgY float64
	viewportExtX, viewportExtY float64
	mapMode                  uint16

	// Objects table (index 0 = null/default)
	objects  [32]*wmfObject
	curPen   *wmfObject
	curBrush *wmfObject
	curPal   *wmfObject

	// State
	bkMode      uint16
	polyFillMode uint16
	textColor   color.RGBA
	bkColor     color.RGBA
	curX, curY  int16

	// SVG output accumulator
	sb strings.Builder
}

func newDC() *dc {
	d := &dc{
		windowExtX:  1, windowExtY:  1,
		viewportExtX: 1, viewportExtY: 1,
		mapMode:     1, // MM_TEXT
		bkMode:      1, // TRANSPARENT
		polyFillMode: 1, // ALTERNATE
		bkColor:     color.RGBA{255, 255, 255, 255},
		textColor:   color.RGBA{0, 0, 0, 255},
	}
	// Pre-fill stock objects (indices 5..7 are common Windows stock objects)
	d.objects[5] = &wmfObject{kind: 1, pen: penObj{col: color.RGBA{0, 0, 0, 255}, width: 1}}
	d.objects[6] = &wmfObject{kind: 2, brush: brushObj{style: 1}} // NULL brush
	d.objects[7] = &wmfObject{kind: 2, brush: brushObj{col: color.RGBA{255, 255, 255, 255}}}
	d.curPen = d.objects[5]
	d.curBrush = d.objects[6]
	return d
}

// lx / ly convert a logical WMF coordinate to the SVG coordinate system.
// Because we set the SVG viewBox to the logical window, the values pass through
// directly — the browser handles the scaling.
func (d *dc) lx(x int16) float64 { return float64(x) }
func (d *dc) ly(y int16) float64 { return float64(y) }

func (d *dc) penCSS() string {
	if d.curPen == nil {
		return `stroke="black" stroke-width="10"`
	}
	p := d.curPen.pen
	if p.style == 5 { // PS_NULL
		return `stroke="none"`
	}
	w := float64(p.width)
	if w <= 0 {
		w = 1
	}
	return fmt.Sprintf(`stroke="%s" stroke-width="%.1f"`, cssColor(p.col), w*2)
}

func (d *dc) fillCSS() string {
	if d.curBrush == nil {
		return `fill="none"`
	}
	b := d.curBrush.brush
	if b.style == 1 { // BS_NULL
		return `fill="none"`
	}
	return fmt.Sprintf(`fill="%s"`, cssColor(b.col))
}

// ToSVG converts a WMF binary blob to an SVG document.
// Returns an error if the data is not valid WMF.
func ToSVG(data []byte) ([]byte, error) {
	if len(data) < 18 {
		return nil, fmt.Errorf("wmfrender: file too short (%d bytes)", len(data))
	}

	// Standard WMF header (18 bytes = 9 words)
	wmfType := le16(data, 0)
	hsize := int(le16(data, 2)) // in words
	// version := le16(data, 4)

	// Validate: type 1 (memory) or 2 (file), header size 9 words
	if (wmfType != 1 && wmfType != 2) || hsize != 9 {
		// Maybe it's an EMF (starts with record type 1 as DWORD, signature at +40)
		if len(data) >= 44 {
			sig := binary.LittleEndian.Uint32(data[40:44])
			if sig == 0x464D4520 { // ' EMF'
				return nil, fmt.Errorf("wmfrender: EMF format not supported")
			}
		}
		return nil, fmt.Errorf("wmfrender: not a valid WMF file (type=%d hsize=%d)", wmfType, hsize)
	}

	ctx := newDC()
	offset := hsize * 2 // skip header

	for offset+6 <= len(data) {
		rsize := int(le32(data, offset))     // record size in words
		rfunc := le16(data, offset+4)        // record function
		if rsize < 3 {
			break
		}
		end := offset + rsize*2
		if end > len(data) {
			end = len(data)
		}
		params := data[offset+6 : end]

		if rfunc == wmfEOF {
			break
		}

		processRecord(ctx, rfunc, params)
		offset = offset + rsize*2
	}

	// Compute viewBox from window extents
	vbX := ctx.windowOrgX
	vbY := ctx.windowOrgY
	vbW := ctx.windowExtX
	vbH := ctx.windowExtY
	if vbW == 0 {
		vbW = 1000
	}
	if vbH == 0 {
		vbH = 1000
	}
	// Ensure positive extents (WMF can have negative extents for flipped axes)
	if vbW < 0 {
		vbX += vbW
		vbW = -vbW
	}
	if vbH < 0 {
		vbY += vbH
		vbH = -vbH
	}

	// Build the final SVG
	bg := cssColor(ctx.bkColor)
	var out strings.Builder
	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	out.WriteString(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="%.3f %.3f %.3f %.3f">`,
		vbX, vbY, vbW, vbH,
	))
	// Background rect
	out.WriteString(fmt.Sprintf(
		`<rect x="%.3f" y="%.3f" width="%.3f" height="%.3f" fill="%s"/>`,
		vbX, vbY, vbW, vbH, bg,
	))
	out.WriteString(ctx.sb.String())
	out.WriteString(`</svg>`)

	return []byte(out.String()), nil
}

// processRecord handles one WMF record.
func processRecord(d *dc, fn uint16, p []byte) {
	switch fn {
	case wmfSetWindowExt:
		if len(p) >= 4 {
			d.windowExtY = float64(int16(le16(p, 0)))
			d.windowExtX = float64(int16(le16(p, 2)))
		}
	case wmfSetWindowOrg:
		if len(p) >= 4 {
			d.windowOrgY = float64(int16(le16(p, 0)))
			d.windowOrgX = float64(int16(le16(p, 2)))
		}
	case wmfOffsetWindowOrg:
		if len(p) >= 4 {
			d.windowOrgY += float64(int16(le16(p, 0)))
			d.windowOrgX += float64(int16(le16(p, 2)))
		}
	case wmfSetViewportExt:
		if len(p) >= 4 {
			d.viewportExtY = float64(int16(le16(p, 0)))
			d.viewportExtX = float64(int16(le16(p, 2)))
		}
	case wmfSetViewportOrg:
		if len(p) >= 4 {
			d.viewportOrgY = float64(int16(le16(p, 0)))
			d.viewportOrgX = float64(int16(le16(p, 2)))
		}
	case wmfOffsetViewportOrg:
		if len(p) >= 4 {
			d.viewportOrgY += float64(int16(le16(p, 0)))
			d.viewportOrgX += float64(int16(le16(p, 2)))
		}
	case wmfScaleViewportExt:
		if len(p) >= 8 {
			yD := float64(int16(le16(p, 0)))
			yN := float64(int16(le16(p, 2)))
			xD := float64(int16(le16(p, 4)))
			xN := float64(int16(le16(p, 6)))
			if xD != 0 {
				d.viewportExtX = d.viewportExtX * xN / xD
			}
			if yD != 0 {
				d.viewportExtY = d.viewportExtY * yN / yD
			}
		}
	case wmfSetMapMode:
		if len(p) >= 2 {
			d.mapMode = le16(p, 0)
		}
	case wmfSetBkMode:
		if len(p) >= 2 {
			d.bkMode = le16(p, 0)
		}
	case wmfSetPolyFillMode:
		if len(p) >= 2 {
			d.polyFillMode = le16(p, 0)
		}
	case wmfSetBkColor:
		if len(p) >= 4 {
			d.bkColor = colorRef(p)
		}
	case wmfSetTextColor:
		if len(p) >= 4 {
			d.textColor = colorRef(p)
		}
	case wmfMoveTo:
		if len(p) >= 4 {
			d.curY = int16(le16(p, 0))
			d.curX = int16(le16(p, 2))
		}
	case wmfCreatePenIndirect:
		if len(p) >= 8 {
			style := le16(p, 0)
			width := int16(le16(p, 2))
			col := colorRef(p[6:])
			obj := &wmfObject{kind: 1, pen: penObj{style: style, width: width, col: col}}
			addObject(d, obj)
		}
	case wmfCreateBrushIndirect:
		if len(p) >= 6 {
			style := le16(p, 0)
			col := colorRef(p[2:])
			obj := &wmfObject{kind: 2, brush: brushObj{style: style, col: col}}
			addObject(d, obj)
		}
	case wmfCreateFontIndirect:
		// We don't need to render text for the current chart WMFs, just track the object
		obj := &wmfObject{kind: 3}
		addObject(d, obj)
	case wmfCreatePaletteIndirect:
		var entries []color.RGBA
		if len(p) >= 4 {
			count := int(le16(p, 2))
			for i := 0; i < count && 4+i*4+3 < len(p); i++ {
				r := p[4+i*4+1]
				g := p[4+i*4+2]
				b := p[4+i*4+3]
				entries = append(entries, color.RGBA{r, g, b, 255})
			}
		}
		obj := &wmfObject{kind: 4, pal: paletteObj{entries: entries}}
		addObject(d, obj)
	case wmfSelectObject:
		if len(p) >= 2 {
			idx := int(le16(p, 0))
			if idx >= 0 && idx < len(d.objects) && d.objects[idx] != nil {
				o := d.objects[idx]
				switch o.kind {
				case 1:
					d.curPen = o
				case 2:
					d.curBrush = o
				case 4:
					d.curPal = o
				}
			}
		}
	case wmfDeleteObject:
		if len(p) >= 2 {
			idx := int(le16(p, 0))
			if idx >= 0 && idx < len(d.objects) {
				// Don't delete stock objects (5–7)
				if idx < 5 || idx > 7 {
					d.objects[idx] = nil
				}
			}
		}
	case wmfPolyLine:
		if len(p) >= 2 {
			count := int(int16(le16(p, 0)))
			renderPolyLine(d, p[2:], count, false)
		}
	case wmfPolygon:
		if len(p) >= 2 {
			count := int(int16(le16(p, 0)))
			renderPolyLine(d, p[2:], count, true)
		}
	case wmfPolyPolygon:
		if len(p) >= 2 {
			polygonCount := int(le16(p, 0))
			if len(p) < 2+polygonCount*2 {
				return
			}
			counts := make([]int, polygonCount)
			for i := 0; i < polygonCount; i++ {
				counts[i] = int(le16(p, 2+i*2))
			}
			pts := p[2+polygonCount*2:]
			for _, c := range counts {
				renderPolyLine(d, pts, c, true)
				pts = pts[c*4:]
			}
		}
	case wmfRectangle:
		if len(p) >= 8 {
			bottom := int16(le16(p, 0))
			right := int16(le16(p, 2))
			top := int16(le16(p, 4))
			left := int16(le16(p, 6))
			x, y := d.lx(left), d.ly(top)
			w := d.lx(right) - x
			h := d.ly(bottom) - y
			rule := fillRule(d.polyFillMode)
			d.sb.WriteString(fmt.Sprintf(
				`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" %s %s fill-rule="%s"/>`,
				x, y, w, h, d.fillCSS(), d.penCSS(), rule,
			))
		}
	case wmfTextOut:
		renderTextOut(d, p)
	case wmfExtTextOut:
		renderExtTextOut(d, p)

	// Records we explicitly ignore
	case wmfEscape, wmfSetROP2, wmfSetStretchBltMode, wmfSetRelAbs,
		wmfSaveDC, wmfRestoreDC, wmfRealizePalette, wmfSetPalEntries,
		wmfSetTextAlign, wmfSetTextCharExtra, wmfSelectPalette, wmfAnimatePalette:
		// no-op

	case wmfScaleWindowExt:
		// handled similarly to ScaleViewportExt if needed, ignore for now
	}
}

// renderPolyLine draws a single polyline or closed polygon.
func renderPolyLine(d *dc, pts []byte, count int, close bool) {
	if count < 2 || len(pts) < count*4 {
		return
	}
	var sb strings.Builder
	for i := 0; i < count; i++ {
		x := d.lx(int16(le16(pts, i*4)))
		y := d.ly(int16(le16(pts, i*4+2)))
		if i == 0 {
			sb.WriteString(fmt.Sprintf("M %.3f %.3f", x, y))
		} else {
			sb.WriteString(fmt.Sprintf(" L %.3f %.3f", x, y))
		}
	}
	if close {
		sb.WriteString(" Z")
		rule := fillRule(d.polyFillMode)
		d.sb.WriteString(fmt.Sprintf(
			`<path d="%s" %s %s fill-rule="%s"/>`,
			sb.String(), d.fillCSS(), d.penCSS(), rule,
		))
	} else {
		d.sb.WriteString(fmt.Sprintf(
			`<path d="%s" fill="none" %s/>`,
			sb.String(), d.penCSS(),
		))
	}
}

// renderTextOut handles META_TEXTOUT (basic text, no options).
func renderTextOut(d *dc, p []byte) {
	if len(p) < 2 {
		return
	}
	slen := int(int16(le16(p, 0)))
	if slen <= 0 || 2+slen > len(p) {
		return
	}
	text := string(p[2 : 2+slen])
	// after string (padded to word boundary): Y then X
	off := 2 + roundUp2(slen)
	if off+4 > len(p) {
		return
	}
	y := d.ly(int16(le16(p, off)))
	x := d.lx(int16(le16(p, off+2)))
	renderText(d, x, y, text)
}

// renderExtTextOut handles META_EXTTEXTOUT.
func renderExtTextOut(d *dc, p []byte) {
	if len(p) < 8 {
		return
	}
	y := d.ly(int16(le16(p, 0)))
	x := d.lx(int16(le16(p, 2)))
	slen := int(le16(p, 4))
	// fwOpts := le16(p, 6) // ignored
	if slen <= 0 || 8+slen > len(p) {
		return
	}
	text := string(p[8 : 8+slen])
	renderText(d, x, y, text)
}

func renderText(d *dc, x, y float64, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	escaped := htmlEscape(text)
	col := cssColor(d.textColor)
	// Approximate font size from the window extent (heuristic)
	fontSize := math.Abs(d.windowExtY) / 40
	if fontSize < 6 {
		fontSize = 12
	}
	d.sb.WriteString(fmt.Sprintf(
		`<text x="%.1f" y="%.1f" fill="%s" font-size="%.1f">%s</text>`,
		x, y, col, fontSize, escaped,
	))
}

// ─── GDI object table helpers ─────────────────────────────────────────────────

func addObject(d *dc, obj *wmfObject) {
	for i := 0; i < len(d.objects); i++ {
		if d.objects[i] == nil {
			d.objects[i] = obj
			return
		}
	}
}

// ─── Utility helpers ──────────────────────────────────────────────────────────

func le16(b []byte, off int) uint16 {
	return binary.LittleEndian.Uint16(b[off : off+2])
}

func le32(b []byte, off int) uint32 {
	return binary.LittleEndian.Uint32(b[off : off+4])
}

// colorRef reads a Windows COLORREF (0x00BBGGRR little-endian).
func colorRef(b []byte) color.RGBA {
	if len(b) < 3 {
		return color.RGBA{0, 0, 0, 255}
	}
	return color.RGBA{R: b[0], G: b[1], B: b[2], A: 255}
}

func cssColor(c color.RGBA) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

func fillRule(mode uint16) string {
	if mode == 2 { // WINDING
		return "nonzero"
	}
	return "evenodd" // ALTERNATE (default)
}

func roundUp2(n int) int {
	if n%2 == 0 {
		return n
	}
	return n + 1
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
