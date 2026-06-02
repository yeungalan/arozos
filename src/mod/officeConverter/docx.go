package officeConverter

import (
	"archive/zip"
	"fmt"
	"html"
	"strconv"
	"strings"
)

// DocxResult holds the HTML output of a DOCX conversion.
type DocxResult struct {
	HTML string `json:"html"`
}

// ConvertDOCX parses a DOCX byte slice and returns an HTML representation.
func ConvertDOCX(data []byte) (*DocxResult, error) {
	files, err := openZip(data)
	if err != nil {
		return nil, fmt.Errorf("open docx: %w", err)
	}

	rels := parseRels(files, "word/_rels/document.xml.rels")
	styles := parseDocxStyles(files)
	numFmts := parseDocxNumbering(files)

	docData, err := readZipEntry(files, "word/document.xml")
	if err != nil {
		return nil, fmt.Errorf("read document.xml: %w", err)
	}
	root, err := parseXMLData(docData)
	if err != nil {
		return nil, fmt.Errorf("parse document.xml: %w", err)
	}

	body := findFirst(root, "body")
	if body == nil {
		return nil, fmt.Errorf("document body not found")
	}

	dx := &docxConverter{files: files, rels: rels, styles: styles, numFmts: numFmts}

	var sb strings.Builder
	sb.WriteString(`<div class="ov-docx-body">`)
	dx.renderBody(&sb, body)
	sb.WriteString(`</div>`)

	return &DocxResult{HTML: sb.String()}, nil
}

// --- Style / numbering parsing ---

func parseDocxStyles(files map[string]*zip.File) map[string]string {
	data, err := readZipEntry(files, "word/styles.xml")
	if err != nil {
		return map[string]string{}
	}
	root, err := parseXMLData(data)
	if err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, style := range root.Nodes {
		if style.XMLName.Local != "style" || style.GetAttr("type") != "paragraph" {
			continue
		}
		styleID := style.GetAttr("styleId")
		nameNode := findFirst(&style, "name")
		if nameNode == nil {
			continue
		}
		level := headingLevel(nameNode.GetAttr("val"), styleID)
		if level != "" {
			out[styleID] = level
		}
	}
	return out
}

func headingLevel(name, styleID string) string {
	lower := strings.ToLower(name)
	for i := 1; i <= 6; i++ {
		s := strconv.Itoa(i)
		if lower == "heading "+s || lower == "heading"+s {
			return s
		}
	}
	lower = strings.ToLower(styleID)
	for i := 1; i <= 6; i++ {
		s := strconv.Itoa(i)
		if lower == "heading"+s {
			return s
		}
	}
	return ""
}

func parseDocxNumbering(files map[string]*zip.File) map[string]string {
	data, err := readZipEntry(files, "word/numbering.xml")
	if err != nil {
		return map[string]string{}
	}
	root, err := parseXMLData(data)
	if err != nil {
		return map[string]string{}
	}

	abstractFmts := map[string]string{}
	for _, an := range findAll(root, "abstractNum") {
		aid := an.GetAttr("abstractNumId")
		lvl := findFirst(an, "lvl")
		if lvl == nil {
			continue
		}
		numFmt := findFirst(lvl, "numFmt")
		if numFmt == nil {
			continue
		}
		if numFmt.GetAttr("val") == "bullet" {
			abstractFmts[aid] = "bullet"
		} else {
			abstractFmts[aid] = "decimal"
		}
	}

	out := map[string]string{}
	for _, num := range findAll(root, "num") {
		nid := num.GetAttr("numId")
		an := findFirst(num, "abstractNumId")
		if an == nil {
			continue
		}
		if f, ok := abstractFmts[an.GetAttr("val")]; ok {
			out[nid] = f
		}
	}
	return out
}

// --- Converter ---

type docxConverter struct {
	files   map[string]*zip.File
	rels    map[string]Relationship
	styles  map[string]string // styleId → heading level "1".."6"
	numFmts map[string]string // numId → "bullet" | "decimal"
}

func (dx *docxConverter) renderBody(sb *strings.Builder, body *XMLNode) {
	var listBuf []*XMLNode
	listType := ""

	flush := func() {
		if len(listBuf) == 0 {
			return
		}
		tag := "ul"
		if listType == "decimal" {
			tag = "ol"
		}
		sb.WriteString(`<` + tag + ` class="ov-list">`)
		for _, p := range listBuf {
			sb.WriteString("<li>")
			dx.renderRunsInto(sb, p)
			sb.WriteString("</li>")
		}
		sb.WriteString(`</` + tag + `>`)
		listBuf = nil
		listType = ""
	}

	for i := range body.Nodes {
		child := &body.Nodes[i]
		switch child.XMLName.Local {
		case "p":
			pPr := findFirst(child, "pPr")
			numID, numFmt := dx.listInfo(pPr)
			if numID != "" {
				if numFmt != listType && len(listBuf) > 0 {
					flush()
				}
				listType = numFmt
				listBuf = append(listBuf, child)
			} else {
				flush()
				sb.WriteString(dx.renderParagraph(child))
			}
		case "tbl":
			flush()
			sb.WriteString(dx.renderTable(child))
		case "sdt":
			flush()
			if content := findFirst(child, "sdtContent"); content != nil {
				dx.renderBody(sb, content)
			}
		}
	}
	flush()
}

func (dx *docxConverter) listInfo(pPr *XMLNode) (numID, numFmt string) {
	numPr := findFirst(pPr, "numPr")
	if numPr == nil {
		return "", ""
	}
	numIDNode := findFirst(numPr, "numId")
	if numIDNode == nil {
		return "", ""
	}
	id := numIDNode.GetAttr("val")
	if id == "0" || id == "" {
		return "", ""
	}
	f := dx.numFmts[id]
	if f == "" {
		f = "bullet"
	}
	return id, f
}

func (dx *docxConverter) renderParagraph(p *XMLNode) string {
	pPr := findFirst(p, "pPr")

	headingTag := ""
	if pPr != nil {
		if styleNode := findFirst(pPr, "pStyle"); styleNode != nil {
			if level, ok := dx.styles[styleNode.GetAttr("val")]; ok {
				headingTag = "h" + level
			}
		}
	}

	var paraStyles []string
	if pPr != nil {
		if jc := findFirst(pPr, "jc"); jc != nil {
			switch jc.GetAttr("val") {
			case "center":
				paraStyles = append(paraStyles, "text-align:center")
			case "right":
				paraStyles = append(paraStyles, "text-align:right")
			case "both":
				paraStyles = append(paraStyles, "text-align:justify")
			}
		}
		if ind := findFirst(pPr, "ind"); ind != nil {
			if left := ind.GetAttr("left"); left != "" {
				if twips, err := strconv.Atoi(left); err == nil && twips > 0 {
					paraStyles = append(paraStyles, fmt.Sprintf("padding-left:%.3fem", float64(twips)/720.0))
				}
			}
		}
	}

	var content strings.Builder
	dx.renderRunsInto(&content, p)
	inner := content.String()

	if strings.TrimSpace(inner) == "" {
		return `<p class="ov-empty">&nbsp;</p>`
	}

	style := ""
	if len(paraStyles) > 0 {
		style = ` style="` + strings.Join(paraStyles, ";") + `"`
	}
	if headingTag != "" {
		return `<` + headingTag + style + `>` + inner + `</` + headingTag + `>`
	}
	return `<p class="ov-p"` + style + `>` + inner + `</p>`
}

// renderRunsInto writes the inline content of a paragraph into sb.
func (dx *docxConverter) renderRunsInto(sb *strings.Builder, p *XMLNode) {
	for i := range p.Nodes {
		child := &p.Nodes[i]
		switch child.XMLName.Local {
		case "r":
			sb.WriteString(dx.renderRun(child))
		case "hyperlink":
			sb.WriteString(dx.renderHyperlink(child))
		case "ins":
			for j := range child.Nodes {
				if child.Nodes[j].XMLName.Local == "r" {
					sb.WriteString(dx.renderRun(&child.Nodes[j]))
				}
			}
		}
	}
}

func (dx *docxConverter) renderRun(r *XMLNode) string {
	rPr := findFirst(r, "rPr")
	var classes []string
	var styles []string

	if rPr != nil {
		if findFirst(rPr, "b") != nil {
			classes = append(classes, "ov-b")
		}
		if findFirst(rPr, "i") != nil {
			classes = append(classes, "ov-i")
		}

		var deco []string
		if u := findFirst(rPr, "u"); u != nil && u.GetAttr("val") != "none" {
			deco = append(deco, "underline")
		}
		if findFirst(rPr, "strike") != nil || findFirst(rPr, "dstrike") != nil {
			deco = append(deco, "line-through")
		}
		if len(deco) > 0 {
			styles = append(styles, "text-decoration:"+strings.Join(deco, " "))
		}

		if sz := findFirst(rPr, "sz"); sz != nil {
			if v, err := strconv.Atoi(sz.GetAttr("val")); err == nil && v > 0 {
				// sz is half-points; 24pt baseline → 1em
				styles = append(styles, fmt.Sprintf("font-size:%.3fem", float64(v)/48.0))
			}
		}

		if color := findFirst(rPr, "color"); color != nil {
			if c := cssColor(color.GetAttr("val")); c != "" {
				styles = append(styles, "color:"+c)
			}
		}

		if hl := findFirst(rPr, "highlight"); hl != nil {
			if bg := highlightToCss(hl.GetAttr("val")); bg != "" {
				styles = append(styles, "background-color:"+bg)
			}
		}

		if fonts := findFirst(rPr, "rFonts"); fonts != nil {
			f := fonts.GetAttr("ascii")
			if f == "" {
				f = fonts.GetAttr("hAnsi")
			}
			if f != "" {
				styles = append(styles, "font-family:"+cssQuote(f))
			}
		}

		if va := findFirst(rPr, "vertAlign"); va != nil {
			switch va.GetAttr("val") {
			case "superscript":
				classes = append(classes, "ov-sup")
			case "subscript":
				classes = append(classes, "ov-sub")
			}
		}
	}

	var parts []string
	for i := range r.Nodes {
		child := &r.Nodes[i]
		switch child.XMLName.Local {
		case "t":
			parts = append(parts, html.EscapeString(child.Text))
		case "br":
			if child.GetAttr("type") == "page" {
				parts = append(parts, `<div class="ov-pagebreak"></div>`)
			} else {
				parts = append(parts, "<br>")
			}
		case "tab":
			parts = append(parts, `&emsp;`)
		case "drawing":
			if url := dx.drawingImageURL(child); url != "" {
				parts = append(parts, `<img class="ov-img" src="`+url+`">`)
			}
		}
	}

	inner := strings.Join(parts, "")
	if inner == "" {
		return ""
	}
	if len(classes) == 0 && len(styles) == 0 {
		return inner
	}
	classAttr := ""
	if len(classes) > 0 {
		classAttr = ` class="` + strings.Join(classes, " ") + `"`
	}
	styleAttr := ""
	if len(styles) > 0 {
		styleAttr = ` style="` + strings.Join(styles, ";") + `"`
	}
	return `<span` + classAttr + styleAttr + `>` + inner + `</span>`
}

func (dx *docxConverter) renderHyperlink(hl *XMLNode) string {
	href := ""
	if rID := hl.GetAttr("id"); rID != "" {
		if rel, ok := dx.rels[rID]; ok {
			href = rel.Target
		}
	}
	if href == "" {
		if anchor := hl.GetAttr("anchor"); anchor != "" {
			href = "#" + anchor
		}
	}

	var inner strings.Builder
	for i := range hl.Nodes {
		if hl.Nodes[i].XMLName.Local == "r" {
			inner.WriteString(dx.renderRun(&hl.Nodes[i]))
		}
	}

	if href != "" {
		return `<a href="` + html.EscapeString(href) + `" target="_blank" rel="noopener noreferrer">` + inner.String() + `</a>`
	}
	return inner.String()
}

func (dx *docxConverter) renderTable(tbl *XMLNode) string {
	var sb strings.Builder
	sb.WriteString(`<table class="ov-table"><tbody>`)
	for _, tr := range findAll(tbl, "tr") {
		sb.WriteString("<tr>")
		for _, tc := range findAll(tr, "tc") {
			var cellAttrs, cellStyles []string
			tcPr := findFirst(tc, "tcPr")
			if tcPr != nil {
				if gs := findFirst(tcPr, "gridSpan"); gs != nil {
					if v, err := strconv.Atoi(gs.GetAttr("val")); err == nil && v > 1 {
						cellAttrs = append(cellAttrs, fmt.Sprintf(`colspan="%d"`, v))
					}
				}
				if shd := findFirst(tcPr, "shd"); shd != nil {
					if bg := cssColor(shd.GetAttr("fill")); bg != "" {
						cellStyles = append(cellStyles, "background-color:"+bg)
					}
				}
				if va := findFirst(tcPr, "vAlign"); va != nil {
					switch va.GetAttr("val") {
					case "center":
						cellStyles = append(cellStyles, "vertical-align:middle")
					case "bottom":
						cellStyles = append(cellStyles, "vertical-align:bottom")
					}
				}
			}

			attrStr := ""
			if len(cellAttrs) > 0 {
				attrStr = " " + strings.Join(cellAttrs, " ")
			}
			styleStr := ""
			if len(cellStyles) > 0 {
				styleStr = ` style="` + strings.Join(cellStyles, ";") + `"`
			}
			sb.WriteString(`<td` + attrStr + styleStr + `>`)
			for j := range tc.Nodes {
				switch tc.Nodes[j].XMLName.Local {
				case "p":
					sb.WriteString(dx.renderParagraph(&tc.Nodes[j]))
				case "tbl":
					sb.WriteString(dx.renderTable(&tc.Nodes[j]))
				}
			}
			sb.WriteString("</td>")
		}
		sb.WriteString("</tr>")
	}
	sb.WriteString("</tbody></table>")
	return sb.String()
}

func (dx *docxConverter) drawingImageURL(drawing *XMLNode) string {
	blip := findDeep(drawing, "blip")
	if blip == nil {
		return ""
	}
	embedID := blip.GetAttr("embed")
	if embedID == "" {
		return ""
	}
	rel, ok := dx.rels[embedID]
	if !ok || !strings.Contains(rel.Type, "/image") {
		return ""
	}
	return imageDataURL(dx.files, resolveMediaPath("word", rel.Target))
}

// --- Helpers ---

func highlightToCss(name string) string {
	switch strings.ToLower(name) {
	case "yellow":
		return "#ffff00"
	case "green":
		return "#00ff00"
	case "cyan":
		return "#00ffff"
	case "magenta":
		return "#ff00ff"
	case "blue":
		return "#0000ff"
	case "red":
		return "#ff0000"
	case "darkblue":
		return "#00008b"
	case "darkcyan":
		return "#008b8b"
	case "darkgreen":
		return "#006400"
	case "darkmagenta":
		return "#8b008b"
	case "darkred":
		return "#8b0000"
	case "darkyellow":
		return "#808000"
	case "darkgray":
		return "#a9a9a9"
	case "lightgray":
		return "#d3d3d3"
	default:
		return ""
	}
}

func cssQuote(s string) string {
	if strings.ContainsAny(s, " ,") {
		return `'` + strings.ReplaceAll(s, `'`, `\'`) + `'`
	}
	return s
}
