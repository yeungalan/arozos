package officeConverter

import (
	"archive/zip"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
)

// XMLNode is a generic XML element tree for parsing OOXML documents.
type XMLNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Nodes   []XMLNode  `xml:",any"`
	Text    string     `xml:",chardata"`
}

// GetAttr returns the value of the attribute matching the given local name.
func (n *XMLNode) GetAttr(local string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// AllText recursively collects all descendant text content.
func (n *XMLNode) AllText() string {
	var b strings.Builder
	b.WriteString(n.Text)
	for i := range n.Nodes {
		b.WriteString(n.Nodes[i].AllText())
	}
	return b.String()
}

// findFirst returns the first direct child with the given local name.
func findFirst(n *XMLNode, local string) *XMLNode {
	if n == nil {
		return nil
	}
	for i := range n.Nodes {
		if n.Nodes[i].XMLName.Local == local {
			return &n.Nodes[i]
		}
	}
	return nil
}

// findAll returns all direct children with the given local name.
func findAll(n *XMLNode, local string) []*XMLNode {
	var out []*XMLNode
	for i := range n.Nodes {
		if n.Nodes[i].XMLName.Local == local {
			out = append(out, &n.Nodes[i])
		}
	}
	return out
}

// findDeep returns the first descendant (depth-first) with the given local name.
func findDeep(n *XMLNode, local string) *XMLNode {
	if n == nil {
		return nil
	}
	for i := range n.Nodes {
		if n.Nodes[i].XMLName.Local == local {
			return &n.Nodes[i]
		}
		if r := findDeep(&n.Nodes[i], local); r != nil {
			return r
		}
	}
	return nil
}

// parseXMLData parses raw XML bytes into an XMLNode tree.
func parseXMLData(data []byte) (*XMLNode, error) {
	var root XMLNode
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	return &root, nil
}

// bytesReaderAt implements io.ReaderAt for a byte slice.
type bytesReaderAt struct{ b []byte }

func (r *bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// openZip opens a byte slice as a zip archive and returns a name→*zip.File map.
func openZip(data []byte) (map[string]*zip.File, error) {
	zr, err := zip.NewReader(&bytesReaderAt{data}, int64(len(data)))
	if err != nil {
		return nil, err
	}
	m := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		m[f.Name] = f
	}
	return m, nil
}

// readZipEntry reads a named file from the zip map (case-insensitive fallback).
func readZipEntry(files map[string]*zip.File, name string) ([]byte, error) {
	f, ok := files[name]
	if !ok {
		ln := strings.ToLower(name)
		for k, v := range files {
			if strings.ToLower(k) == ln {
				f = v
				ok = true
				break
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("entry %q not found in archive", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// Relationship holds one entry from a .rels file.
type Relationship struct {
	ID         string
	Type       string
	Target     string
	TargetMode string
}

// parseRels reads a .rels XML file and returns an ID→Relationship map.
func parseRels(files map[string]*zip.File, relsPath string) map[string]Relationship {
	out := map[string]Relationship{}
	data, err := readZipEntry(files, relsPath)
	if err != nil {
		return out
	}
	var doc struct {
		Items []struct {
			ID         string `xml:"Id,attr"`
			Type       string `xml:"Type,attr"`
			Target     string `xml:"Target,attr"`
			TargetMode string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return out
	}
	for _, it := range doc.Items {
		out[it.ID] = Relationship{it.ID, it.Type, it.Target, it.TargetMode}
	}
	return out
}

// imageDataURL reads a media file from the zip and returns a base64 data URL,
// or empty string on failure/unsupported format.
func imageDataURL(files map[string]*zip.File, zipPath string) string {
	data, err := readZipEntry(files, zipPath)
	if err != nil {
		return ""
	}
	ext := strings.ToLower(path.Ext(zipPath))
	mime := mimeForExt(ext)
	if mime == "" {
		return ""
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func mimeForExt(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".tiff", ".tif":
		return "image/tiff"
	default:
		return ""
	}
}

// resolveMediaPath converts a relationship target (e.g. "media/image1.png")
// relative to basePath (e.g. "word") into a full zip path.
func resolveMediaPath(basePath, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	return path.Join(basePath, target)
}

// cssColor converts an OOXML hex color (with or without # or ARGB prefix) to
// a CSS hex color, returning "" for auto/transparent/empty.
func cssColor(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	if hex == "" || strings.EqualFold(hex, "auto") {
		return ""
	}
	// Strip alpha prefix from ARGB (OOXML stores as AARRGGBB)
	if len(hex) == 8 {
		hex = hex[2:]
	}
	if len(hex) == 6 && strings.EqualFold(hex, "ffffff") {
		// Transparent-ish: keep white if explicit
		return "#" + strings.ToLower(hex)
	}
	if len(hex) != 6 {
		return ""
	}
	return "#" + strings.ToLower(hex)
}
