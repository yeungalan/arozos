package officeConverter

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// PPTXResult holds structured slide data for client-side rendering.
type PPTXResult struct {
	Width  int64      `json:"width"`  // in EMU
	Height int64      `json:"height"` // in EMU
	Slides []PPTXSlide `json:"slides"`
}

// PPTXSlide represents a single presentation slide.
type PPTXSlide struct {
	Background string        `json:"bg"`
	Elements   []PPTXElement `json:"elements"`
}

// PPTXElement is one visual element on a slide.
type PPTXElement struct {
	Type       string          `json:"type"`            // "text" | "image" | "shape"
	X          int64           `json:"x"`               // offset left in EMU
	Y          int64           `json:"y"`               // offset top in EMU
	W          int64           `json:"w"`               // width in EMU
	H          int64           `json:"h"`               // height in EMU
	Paragraphs []PPTXParagraph `json:"paragraphs,omitempty"`
	VAlign     string          `json:"vAlign,omitempty"` // "t"|"ctr"|"b"
	Src        string          `json:"src,omitempty"`    // base64 data URL for images
	FillColor  string          `json:"fill,omitempty"`   // shape fill color
}

// PPTXParagraph is a paragraph within a text element.
type PPTXParagraph struct {
	Align string    `json:"align,omitempty"` // "l"|"ctr"|"r"|"just"
	Runs  []PPTXRun `json:"runs"`
}

// PPTXRun is a text run with formatting.
type PPTXRun struct {
	Text   string `json:"text"`
	Bold   bool   `json:"bold,omitempty"`
	Italic bool   `json:"italic,omitempty"`
	Under  bool   `json:"under,omitempty"`
	Size   int    `json:"size,omitempty"`  // in hundredths of a point
	Color  string `json:"color,omitempty"` // CSS color
	Font   string `json:"font,omitempty"`
}

// ConvertPPTX parses a PPTX byte slice and returns structured slide data.
func ConvertPPTX(data []byte) (*PPTXResult, error) {
	files, err := openZip(data)
	if err != nil {
		return nil, fmt.Errorf("open pptx: %w", err)
	}

	width, height, slideRels, err := parsePresentationMeta(files)
	if err != nil {
		return nil, fmt.Errorf("parse presentation: %w", err)
	}

	var slides []PPTXSlide
	for _, info := range slideRels {
		slide, err := parseSlide(files, info.zipPath, info.relsPath)
		if err != nil {
			// Include a blank slide rather than failing entirely
			slides = append(slides, PPTXSlide{Background: "#ffffff"})
			continue
		}
		slides = append(slides, slide)
	}

	return &PPTXResult{Width: width, Height: height, Slides: slides}, nil
}

// --- Presentation metadata ---

type slidePathInfo struct {
	zipPath  string
	relsPath string
}

func parsePresentationMeta(files map[string]*zip.File) (width, height int64, slides []slidePathInfo, err error) {
	data, err := readZipEntry(files, "ppt/presentation.xml")
	if err != nil {
		return 0, 0, nil, err
	}
	root, err := parseXMLData(data)
	if err != nil {
		return 0, 0, nil, err
	}

	// Slide size
	sldSz := findDeep(root, "sldSz")
	if sldSz != nil {
		width, _ = strconv.ParseInt(sldSz.GetAttr("cx"), 10, 64)
		height, _ = strconv.ParseInt(sldSz.GetAttr("cy"), 10, 64)
	}
	if width == 0 {
		width = 9144000
	}
	if height == 0 {
		height = 6858000
	}

	// Slide list (ordered)
	rels := parseRels(files, "ppt/_rels/presentation.xml.rels")
	sldIdLst := findDeep(root, "sldIdLst")
	if sldIdLst == nil {
		return width, height, nil, nil
	}
	for _, sldID := range findAll(sldIdLst, "sldId") {
		rID := sldID.GetAttr("id")
		rel, ok := rels[rID]
		if !ok {
			continue
		}
		target := rel.Target
		if strings.HasPrefix(target, "../") {
			target = "ppt/" + target[3:]
		} else if !strings.HasPrefix(target, "ppt/") {
			target = "ppt/" + target
		}
		// Build rels path: ppt/slides/_rels/slide1.xml.rels
		parts := strings.Split(target, "/")
		base := parts[len(parts)-1]
		dir := strings.Join(parts[:len(parts)-1], "/")
		relsPath := dir + "/_rels/" + base + ".rels"
		slides = append(slides, slidePathInfo{zipPath: target, relsPath: relsPath})
	}
	return
}

// --- Slide parsing ---

func parseSlide(files map[string]*zip.File, slidePath, relsPath string) (PPTXSlide, error) {
	data, err := readZipEntry(files, slidePath)
	if err != nil {
		return PPTXSlide{}, err
	}
	root, err := parseXMLData(data)
	if err != nil {
		return PPTXSlide{}, err
	}

	rels := parseRels(files, relsPath)
	slideDir := slideBaseDir(slidePath)

	slide := PPTXSlide{Background: "#ffffff"}

	// Background color
	bg := extractSlideBg(root, files, slideDir)
	if bg != "" {
		slide.Background = bg
	}

	// Shape tree
	spTree := findDeep(root, "spTree")
	if spTree == nil {
		return slide, nil
	}

	for i := range spTree.Nodes {
		node := &spTree.Nodes[i]
		switch node.XMLName.Local {
		case "sp": // Shape (text box, placeholder, etc.)
			if el, ok := parseShape(node); ok {
				slide.Elements = append(slide.Elements, el)
			}
		case "pic": // Picture
			if el, ok := parsePicture(node, files, rels, slideDir); ok {
				slide.Elements = append(slide.Elements, el)
			}
		case "graphicFrame":
			// Tables, charts etc. — extract any text we can
			if el, ok := parseGraphicFrame(node); ok {
				slide.Elements = append(slide.Elements, el)
			}
		}
	}

	return slide, nil
}

func slideBaseDir(slidePath string) string {
	parts := strings.Split(slidePath, "/")
	return strings.Join(parts[:len(parts)-1], "/")
}

// extractSlideBg tries to determine the slide background color from the slide XML.
func extractSlideBg(root *XMLNode, files map[string]*zip.File, slideDir string) string {
	cSld := findDeep(root, "cSld")
	if cSld == nil {
		return ""
	}
	bg := findFirst(cSld, "bg")
	if bg == nil {
		return ""
	}
	bgPr := findFirst(bg, "bgPr")
	if bgPr == nil {
		return ""
	}
	solidFill := findFirst(bgPr, "solidFill")
	if solidFill == nil {
		return ""
	}
	return extractFillColor(solidFill)
}

func extractFillColor(fill *XMLNode) string {
	if fill == nil {
		return ""
	}
	// srgbClr or sysClr or schemeClr
	if rgb := findFirst(fill, "srgbClr"); rgb != nil {
		return cssColor(rgb.GetAttr("val"))
	}
	if sys := findFirst(fill, "sysClr"); sys != nil {
		return cssColor(sys.GetAttr("lastClr"))
	}
	return ""
}

// --- Shape (text box / placeholder) ---

func parseShape(sp *XMLNode) (PPTXElement, bool) {
	spPr := findFirst(sp, "spPr")
	x, y, w, h := extractXfrm(spPr)

	txBody := findFirst(sp, "txBody")
	if txBody == nil {
		// Plain shape with fill only
		el := PPTXElement{Type: "shape", X: x, Y: y, W: w, H: h}
		if solidFill := findDeep(spPr, "solidFill"); solidFill != nil {
			el.FillColor = extractFillColor(solidFill)
		}
		if el.FillColor == "" {
			return el, false
		}
		return el, true
	}

	el := PPTXElement{Type: "text", X: x, Y: y, W: w, H: h}

	// Vertical alignment from bodyPr
	bodyPr := findFirst(txBody, "bodyPr")
	if bodyPr != nil {
		el.VAlign = bodyPr.GetAttr("anchor")
	}

	// Default run properties (from lstStyle or txBody defRPr)
	defRPr := findDeep(txBody, "defRPr")

	for _, para := range findAll(txBody, "p") {
		p := parseParagraphPPTX(para, defRPr)
		if len(p.Runs) > 0 || len(el.Paragraphs) == 0 {
			el.Paragraphs = append(el.Paragraphs, p)
		}
	}

	if len(el.Paragraphs) == 0 {
		return el, false
	}
	return el, true
}

func parseParagraphPPTX(para *XMLNode, defRPr *XMLNode) PPTXParagraph {
	p := PPTXParagraph{}

	pPr := findFirst(para, "pPr")
	if pPr != nil {
		switch pPr.GetAttr("algn") {
		case "ctr":
			p.Align = "center"
		case "r":
			p.Align = "right"
		case "just":
			p.Align = "justify"
		default:
			p.Align = "left"
		}
	}

	// Paragraph-level default rPr
	paraDefRPr := findFirst(pPr, "defRPr")
	if paraDefRPr == nil {
		paraDefRPr = defRPr
	}

	for _, child := range para.Nodes {
		switch child.XMLName.Local {
		case "r":
			run := parseRunPPTX(&child, paraDefRPr)
			if run.Text != "" {
				p.Runs = append(p.Runs, run)
			}
		case "br":
			// Line break
			p.Runs = append(p.Runs, PPTXRun{Text: "\n"})
		}
	}
	return p
}

func parseRunPPTX(r *XMLNode, defRPr *XMLNode) PPTXRun {
	run := PPTXRun{}

	tNode := findFirst(r, "t")
	if tNode == nil {
		return run
	}
	run.Text = tNode.Text

	rPr := findFirst(r, "rPr")
	if rPr == nil {
		rPr = defRPr
	}
	if rPr == nil {
		return run
	}

	if rPr.GetAttr("b") == "1" {
		run.Bold = true
	}
	if rPr.GetAttr("i") == "1" {
		run.Italic = true
	}
	if u := rPr.GetAttr("u"); u != "" && u != "none" {
		run.Under = true
	}
	if sz := rPr.GetAttr("sz"); sz != "" {
		run.Size, _ = strconv.Atoi(sz) // hundredths of a point
	}

	if solidFill := findFirst(rPr, "solidFill"); solidFill != nil {
		run.Color = extractFillColor(solidFill)
	}

	if latin := findFirst(rPr, "latin"); latin != nil {
		run.Font = latin.GetAttr("typeface")
	}

	return run
}

// --- Picture ---

func parsePicture(pic *XMLNode, files map[string]*zip.File, rels map[string]Relationship, slideDir string) (PPTXElement, bool) {
	spPr := findFirst(pic, "spPr")
	x, y, w, h := extractXfrm(spPr)

	blipFill := findFirst(pic, "blipFill")
	if blipFill == nil {
		return PPTXElement{}, false
	}
	blip := findFirst(blipFill, "blip")
	if blip == nil {
		return PPTXElement{}, false
	}

	embedID := blip.GetAttr("embed")
	if embedID == "" {
		return PPTXElement{}, false
	}
	rel, ok := rels[embedID]
	if !ok || !strings.Contains(rel.Type, "/image") {
		return PPTXElement{}, false
	}

	target := rel.Target
	if strings.HasPrefix(target, "../") {
		target = "ppt/" + target[3:]
	} else if !strings.HasPrefix(target, "ppt/") {
		target = slideDir + "/" + target
	}

	dataURL := imageDataURL(files, target)
	if dataURL == "" {
		return PPTXElement{}, false
	}

	return PPTXElement{
		Type: "image",
		X: x, Y: y, W: w, H: h,
		Src: dataURL,
	}, true
}

// --- Graphic frame (tables, charts) ---

func parseGraphicFrame(gf *XMLNode) (PPTXElement, bool) {
	xfrm := findDeep(gf, "xfrm")
	var x, y, w, h int64
	if xfrm != nil {
		off := findFirst(xfrm, "off")
		ext := findFirst(xfrm, "ext")
		if off != nil {
			x, _ = strconv.ParseInt(off.GetAttr("x"), 10, 64)
			y, _ = strconv.ParseInt(off.GetAttr("y"), 10, 64)
		}
		if ext != nil {
			w, _ = strconv.ParseInt(ext.GetAttr("cx"), 10, 64)
			h, _ = strconv.ParseInt(ext.GetAttr("cy"), 10, 64)
		}
	}

	// Try to extract table rows
	tbl := findDeep(gf, "tbl")
	if tbl == nil {
		return PPTXElement{}, false
	}

	var paras []PPTXParagraph
	for _, tr := range findAll(tbl, "tr") {
		for _, tc := range findAll(tr, "tc") {
			txBody := findFirst(tc, "txBody")
			if txBody == nil {
				continue
			}
			for _, para := range findAll(txBody, "p") {
				p := parseParagraphPPTX(para, nil)
				if len(p.Runs) > 0 {
					paras = append(paras, p)
				}
			}
		}
	}

	if len(paras) == 0 {
		return PPTXElement{}, false
	}
	return PPTXElement{
		Type: "text", X: x, Y: y, W: w, H: h,
		Paragraphs: paras,
	}, true
}

// --- Transform extraction ---

func extractXfrm(spPr *XMLNode) (x, y, w, h int64) {
	if spPr == nil {
		return
	}
	xfrm := findFirst(spPr, "xfrm")
	if xfrm == nil {
		xfrm = findDeep(spPr, "xfrm")
	}
	if xfrm == nil {
		return
	}
	off := findFirst(xfrm, "off")
	ext := findFirst(xfrm, "ext")
	if off != nil {
		x, _ = strconv.ParseInt(off.GetAttr("x"), 10, 64)
		y, _ = strconv.ParseInt(off.GetAttr("y"), 10, 64)
	}
	if ext != nil {
		w, _ = strconv.ParseInt(ext.GetAttr("cx"), 10, 64)
		h, _ = strconv.ParseInt(ext.GetAttr("cy"), 10, 64)
	}
	return
}

// --- Streaming XML helper used by XLSX (also useful here) ---

// parseXMLStream is a helper that tokenises an XML byte slice without
// building a full tree, returning tokens to a callback.
// Not used directly but kept for reference.
func parseXMLStream(data []byte, cb func(xml.Token)) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		cb(tok)
	}
}
