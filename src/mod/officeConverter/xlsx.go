package officeConverter

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// XLSXResult holds the JSON output of an XLSX conversion.
type XLSXResult struct {
	Sheets []XLSXSheet `json:"sheets"`
}

// XLSXSheet represents a single worksheet.
type XLSXSheet struct {
	Name      string      `json:"name"`
	Rows      [][]XLSXCell `json:"rows"`
	ColWidths []float64   `json:"colWidths"` // in characters
	Merges    []XLSXMerge `json:"merges"`
	MaxRow    int         `json:"maxRow"`
	MaxCol    int         `json:"maxCol"`
}

// XLSXCell represents a spreadsheet cell.
type XLSXCell struct {
	V     string `json:"v"`              // display value
	T     string `json:"t,omitempty"`    // "s"=string,"n"=number,"b"=bool,"e"=error
	Bold  bool   `json:"bold,omitempty"`
	Italic bool  `json:"italic,omitempty"`
	Under  bool  `json:"under,omitempty"`
	FG    string `json:"fg,omitempty"`   // text color
	BG    string `json:"bg,omitempty"`   // background color
	Align string `json:"align,omitempty"` // left|center|right
}

// XLSXMerge represents a merged cell range (0-based).
type XLSXMerge struct {
	R1 int `json:"r1"`
	C1 int `json:"c1"`
	R2 int `json:"r2"`
	C2 int `json:"c2"`
}

// xlsxStyle holds parsed style information for a cell xf index.
type xlsxStyle struct {
	Bold    bool
	Italic  bool
	Under   bool
	FG      string
	BG      string
	Align   string
	NumFmtID int
}

// ConvertXLSX parses an XLSX byte slice and returns a structured result.
func ConvertXLSX(data []byte) (*XLSXResult, error) {
	files, err := openZip(data)
	if err != nil {
		return nil, fmt.Errorf("open xlsx: %w", err)
	}

	sharedStrings := parseSharedStrings(files)
	styles, numFmts := parseXLSXStyles(files)
	sheetInfos, err := parseWorkbook(files)
	if err != nil {
		return nil, fmt.Errorf("parse workbook: %w", err)
	}

	var sheets []XLSXSheet
	for _, info := range sheetInfos {
		sheet, err := parseWorksheet(files, info.Name, info.Path, sharedStrings, styles, numFmts)
		if err != nil {
			continue
		}
		sheets = append(sheets, sheet)
	}
	if len(sheets) == 0 {
		return nil, fmt.Errorf("no sheets found")
	}
	return &XLSXResult{Sheets: sheets}, nil
}

// --- Workbook parsing ---

type sheetInfo struct {
	Name string
	Path string
}

func parseWorkbook(files map[string]*zip.File) ([]sheetInfo, error) {
	data, err := readZipEntry(files, "xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseXMLData(data)
	if err != nil {
		return nil, err
	}

	rels := parseRels(files, "xl/_rels/workbook.xml.rels")

	sheets := findDeep(root, "sheets")
	if sheets == nil {
		return nil, fmt.Errorf("sheets element not found")
	}

	var infos []sheetInfo
	for _, sh := range findAll(sheets, "sheet") {
		name := sh.GetAttr("name")
		rID := sh.GetAttr("id")
		rel, ok := rels[rID]
		if !ok {
			continue
		}
		target := rel.Target
		if !strings.HasPrefix(target, "xl/") {
			target = "xl/" + target
		}
		infos = append(infos, sheetInfo{Name: name, Path: target})
	}
	return infos, nil
}

// --- Shared strings ---

func parseSharedStrings(files map[string]*zip.File) []string {
	data, err := readZipEntry(files, "xl/sharedStrings.xml")
	if err != nil {
		return nil
	}
	root, err := parseXMLData(data)
	if err != nil {
		return nil
	}
	var out []string
	for _, si := range findAll(root, "si") {
		out = append(out, si.AllText())
	}
	return out
}

// --- Styles parsing ---

func parseXLSXStyles(files map[string]*zip.File) ([]xlsxStyle, map[int]string) {
	data, err := readZipEntry(files, "xl/styles.xml")
	if err != nil {
		return nil, nil
	}
	root, err := parseXMLData(data)
	if err != nil {
		return nil, nil
	}

	// Parse number formats
	numFmtsMap := map[int]string{}
	numFmtsNode := findDeep(root, "numFmts")
	if numFmtsNode != nil {
		for _, nf := range findAll(numFmtsNode, "numFmt") {
			id, _ := strconv.Atoi(nf.GetAttr("numFmtId"))
			numFmtsMap[id] = nf.GetAttr("formatCode")
		}
	}

	// Parse fonts
	type fontInfo struct {
		Bold   bool
		Italic bool
		Under  bool
		Color  string
	}
	var fonts []fontInfo
	fontsNode := findDeep(root, "fonts")
	if fontsNode != nil {
		for _, fnt := range findAll(fontsNode, "font") {
			fi := fontInfo{}
			if findFirst(fnt, "b") != nil {
				fi.Bold = true
			}
			if findFirst(fnt, "i") != nil {
				fi.Italic = true
			}
			if u := findFirst(fnt, "u"); u != nil && u.GetAttr("val") != "none" {
				fi.Under = true
			}
			if color := findFirst(fnt, "color"); color != nil {
				fi.Color = resolveThemeOrRGB(color, root)
			}
			fonts = append(fonts, fi)
		}
	}

	// Parse fills
	var fills []string // bg color per fill index
	fillsNode := findDeep(root, "fills")
	if fillsNode != nil {
		for _, fill := range findAll(fillsNode, "fill") {
			pf := findFirst(fill, "patternFill")
			bg := ""
			if pf != nil {
				fg := findFirst(pf, "fgColor")
				if fg != nil {
					bg = cssColor(fg.GetAttr("rgb"))
					if bg == "" {
						bg = cssColor(fg.GetAttr("theme"))
					}
				}
			}
			fills = append(fills, bg)
		}
	}

	// Parse cell xf table (cellXfs)
	cellXfsNode := findDeep(root, "cellXfs")
	if cellXfsNode == nil {
		return nil, numFmtsMap
	}

	var out []xlsxStyle
	for _, xf := range findAll(cellXfsNode, "xf") {
		s := xlsxStyle{}
		fontID, _ := strconv.Atoi(xf.GetAttr("fontId"))
		fillID, _ := strconv.Atoi(xf.GetAttr("fillId"))
		s.NumFmtID, _ = strconv.Atoi(xf.GetAttr("numFmtId"))

		if fontID >= 0 && fontID < len(fonts) {
			fi := fonts[fontID]
			s.Bold = fi.Bold
			s.Italic = fi.Italic
			s.Under = fi.Under
			s.FG = fi.Color
		}
		if fillID >= 0 && fillID < len(fills) {
			s.BG = fills[fillID]
		}

		if al := findFirst(xf, "alignment"); al != nil {
			switch al.GetAttr("horizontal") {
			case "center":
				s.Align = "center"
			case "right":
				s.Align = "right"
			case "left":
				s.Align = "left"
			}
		}
		out = append(out, s)
	}
	return out, numFmtsMap
}

func resolveThemeOrRGB(node *XMLNode, root *XMLNode) string {
	if rgb := node.GetAttr("rgb"); rgb != "" {
		return cssColor(rgb)
	}
	return ""
}

// --- Worksheet parsing ---

func parseWorksheet(
	files map[string]*zip.File,
	name, zipPath string,
	sharedStrings []string,
	styles []xlsxStyle,
	numFmts map[int]string,
) (XLSXSheet, error) {
	data, err := readZipEntry(files, zipPath)
	if err != nil {
		return XLSXSheet{}, err
	}

	// Use streaming parser for potentially large sheets
	dec := xml.NewDecoder(strings.NewReader(string(data)))

	type cellData struct {
		row, col int
		value    string
		typ      string
		styleIdx int
	}

	var cells []cellData
	var merges []XLSXMerge
	colWidths := map[int]float64{}
	maxRow, maxCol := 0, 0

	inSheetData := false
	inRow := false
	curRow := 0
	curCell := struct {
		row, col int
		t        string
		s        int
		vBuf     strings.Builder
		inV      bool
	}{}

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "sheetData":
				inSheetData = true
			case "row":
				if inSheetData {
					inRow = true
					for _, a := range t.Attr {
						if a.Name.Local == "r" {
							curRow, _ = strconv.Atoi(a.Value)
							curRow-- // 0-based
						}
					}
				}
			case "c":
				if inRow {
					curCell.vBuf.Reset()
					curCell.inV = false
					curCell.t = ""
					curCell.s = 0
					for _, a := range t.Attr {
						switch a.Name.Local {
						case "r":
							_, col := cellRefToRowCol(a.Value)
							curCell.row = curRow
							curCell.col = col
						case "t":
							curCell.t = a.Value
						case "s":
							curCell.s, _ = strconv.Atoi(a.Value)
						}
					}
				}
			case "v", "t":
				if inRow {
					curCell.inV = true
				}
			case "col":
				if !inSheetData {
					minCol, maxColAttr := 0, 0
					w := 0.0
					for _, a := range t.Attr {
						switch a.Name.Local {
						case "min":
							minCol, _ = strconv.Atoi(a.Value)
							minCol--
						case "max":
							maxColAttr, _ = strconv.Atoi(a.Value)
							maxColAttr--
						case "width":
							w, _ = strconv.ParseFloat(a.Value, 64)
						}
					}
					for c := minCol; c <= maxColAttr; c++ {
						colWidths[c] = w
					}
				}
			case "mergeCell":
				for _, a := range t.Attr {
					if a.Name.Local == "ref" {
						m := parseMergeRef(a.Value)
						merges = append(merges, m)
					}
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "sheetData":
				inSheetData = false
			case "row":
				inRow = false
			case "c":
				if inRow {
					raw := curCell.vBuf.String()
					value := ""
					typ := "s"

					switch curCell.t {
					case "s":
						// Shared string index
						if idx, err := strconv.Atoi(raw); err == nil && idx < len(sharedStrings) {
							value = sharedStrings[idx]
						}
						typ = "s"
					case "b":
						if raw == "1" {
							value = "TRUE"
						} else {
							value = "FALSE"
						}
						typ = "b"
					case "e":
						value = raw
						typ = "e"
					case "str", "inlineStr":
						value = raw
						typ = "s"
					default:
						// Number or date
						value = formatNumber(raw, curCell.s, styles, numFmts)
						typ = "n"
					}

					if curCell.row > maxRow {
						maxRow = curCell.row
					}
					if curCell.col > maxCol {
						maxCol = curCell.col
					}

					cells = append(cells, cellData{
						row: curCell.row, col: curCell.col,
						value: value, typ: typ, styleIdx: curCell.s,
					})
				}
				curCell.inV = false
			case "v", "t":
				curCell.inV = false
			}
		case xml.CharData:
			if curCell.inV {
				curCell.vBuf.Write(t)
			}
		}
	}

	// Build 2D row/col grid
	rows := make([][]XLSXCell, maxRow+1)
	for i := range rows {
		rows[i] = make([]XLSXCell, maxCol+1)
	}

	for _, cd := range cells {
		if cd.row < 0 || cd.col < 0 || cd.row > maxRow || cd.col > maxCol {
			continue
		}
		cell := XLSXCell{V: cd.value, T: cd.typ}
		if cd.styleIdx < len(styles) {
			st := styles[cd.styleIdx]
			cell.Bold = st.Bold
			cell.Italic = st.Italic
			cell.Under = st.Under
			cell.FG = st.FG
			cell.BG = st.BG
			cell.Align = st.Align
		}
		rows[cd.row][cd.col] = cell
	}

	// Build column widths slice
	cwSlice := make([]float64, maxCol+1)
	for i := range cwSlice {
		if w, ok := colWidths[i]; ok {
			cwSlice[i] = w
		} else {
			cwSlice[i] = 8.43 // Excel default
		}
	}

	return XLSXSheet{
		Name:      name,
		Rows:      rows,
		ColWidths: cwSlice,
		Merges:    merges,
		MaxRow:    maxRow,
		MaxCol:    maxCol,
	}, nil
}

// formatNumber converts a raw numeric string to a display value given the style index.
func formatNumber(raw string, styleIdx int, styles []xlsxStyle, numFmts map[int]string) string {
	if raw == "" {
		return ""
	}
	val, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}

	fmtID := 0
	if styleIdx < len(styles) {
		fmtID = styles[styleIdx].NumFmtID
	}

	// Check if it's a date format
	if isDateFormat(fmtID, numFmts) {
		t := excelSerial(val)
		switch fmtID {
		case 14, 15, 16, 17:
			return t.Format("2006-01-02")
		case 18, 19:
			return t.Format("3:04 PM")
		case 20, 21:
			return t.Format("15:04")
		case 22:
			return t.Format("2006-01-02 15:04")
		default:
			return t.Format("2006-01-02")
		}
	}

	// Built-in numeric formats
	switch fmtID {
	case 0: // General
		if val == math.Trunc(val) && math.Abs(val) < 1e15 {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'g', -1, 64)
	case 1:
		return strconv.FormatInt(int64(math.Round(val)), 10)
	case 2:
		return fmt.Sprintf("%.2f", val)
	case 3:
		return formatWithCommas(int64(math.Round(val)))
	case 4:
		return formatWithCommasF(val, 2)
	case 9:
		return fmt.Sprintf("%.0f%%", val*100)
	case 10:
		return fmt.Sprintf("%.2f%%", val*100)
	case 11:
		return fmt.Sprintf("%.2E", val)
	case 49: // Text
		return raw
	}

	// Custom format — just return the number nicely
	if val == math.Trunc(val) && math.Abs(val) < 1e15 {
		return strconv.FormatInt(int64(val), 10)
	}
	return strconv.FormatFloat(val, 'f', 2, 64)
}

func isDateFormat(fmtID int, custom map[int]string) bool {
	if fmtID >= 14 && fmtID <= 22 {
		return true
	}
	if fmtID >= 45 && fmtID <= 47 {
		return true
	}
	// Check custom format string for date tokens
	if code, ok := custom[fmtID]; ok {
		lower := strings.ToLower(code)
		for _, token := range []string{"yy", "mm", "dd", "hh", "ss"} {
			if strings.Contains(lower, token) {
				return true
			}
		}
	}
	return false
}

// excelSerial converts an Excel date serial number to time.Time.
func excelSerial(serial float64) time.Time {
	// Excel has a deliberate bug treating 1900 as a leap year,
	// so serials > 59 are off by one.
	if serial >= 60 {
		serial--
	}
	// Base date: December 30, 1899
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	days := int(serial)
	frac := serial - float64(days)
	return base.AddDate(0, 0, days).Add(time.Duration(frac * float64(24*time.Hour)))
}

func formatWithCommas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := false
	if n < 0 {
		neg = true
		s = s[1:]
	}
	var out []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

func formatWithCommasF(f float64, decimals int) string {
	whole := int64(math.Trunc(f))
	dec := f - float64(whole)
	s := formatWithCommas(whole)
	return s + fmt.Sprintf("%.*f", decimals, dec)[1:] // add .XX
}

// cellRefToRowCol converts "A1" → row=0, col=0 (both 0-based).
func cellRefToRowCol(ref string) (row, col int) {
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		i++
	}
	col = colLettersToIndex(ref[:i])
	row, _ = strconv.Atoi(ref[i:])
	row-- // 0-based
	return
}

func colLettersToIndex(s string) int {
	result := 0
	for _, c := range s {
		result = result*26 + int(c-'A'+1)
	}
	return result - 1
}

// parseMergeRef parses "A1:C3" into an XLSXMerge (0-based).
func parseMergeRef(ref string) XLSXMerge {
	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 {
		return XLSXMerge{}
	}
	r1, c1 := cellRefToRowCol(parts[0])
	r2, c2 := cellRefToRowCol(parts[1])
	return XLSXMerge{R1: r1, C1: c1, R2: r2, C2: c2}
}
