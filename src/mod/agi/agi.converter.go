package agi

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/robertkrimen/otto"
	uuid "github.com/satori/go.uuid"

	"imuslab.com/arozos/mod/agi/static"
	"imuslab.com/arozos/mod/filesystem"
	"imuslab.com/arozos/mod/filesystem/metadata"
	"imuslab.com/arozos/mod/info/logger"
	"imuslab.com/arozos/mod/utils"
)

/*
	AGI File Converter Library

	This library converts source files that browsers cannot display directly
	(camera RAW photos and PDF documents) into plain JPEG images, all through
	the arozos virtualized file system layer.

	RAW conversion (ARW / CR2 / DNG / NEF / RAF / ORF) is performed purely in
	Go by extracting the embedded full-resolution JPEG preview, so it is always
	available.

	PDF conversion prefers a host PDF rasterizer (pdftoppm, pdftocairo, mutool
	or ghostscript) when one is installed, giving faithful rendering of vector
	and text pages. When no rasterizer is present it falls back to extracting
	the largest embedded JPEG image from the PDF, which works for scanned /
	image-based documents.

	Author: tobychui
*/

// pdfRasterizers lists the external PDF rasterizer tools this library knows how
// to drive, in order of preference.
var pdfRasterizers = []string{"pdftoppm", "pdftocairo", "mutool", "gs"}

func (g *Gateway) ConverterLibRegister() {
	err := g.RegisterLib("converter", g.injectConverterFunctions)
	if err != nil {
		logger.PrintAndLog("Agi", fmt.Sprint(err), nil)
		return
	}
}

func (g *Gateway) injectConverterFunctions(payload *static.AgiLibInjectionPayload) {
	vm := payload.VM
	u := payload.User
	scriptFsh := payload.ScriptFsh

	// resolveSrcDest rewrites relative vpaths and translates the source and
	// destination virtual paths to (fsh, realpath) pairs.
	resolveSrcDest := func(vsrc, vdest string) (*filesystem.FileSystemHandler, string, *filesystem.FileSystemHandler, string, error) {
		vsrc = static.RelativeVpathRewrite(scriptFsh, vsrc, vm, u)
		vdest = static.RelativeVpathRewrite(scriptFsh, vdest, vm, u)
		srcFsh, rsrc, err := static.VirtualPathToRealPath(vsrc, u)
		if err != nil {
			return nil, "", nil, "", err
		}
		destFsh, rdest, err := static.VirtualPathToRealPath(vdest, u)
		if err != nil {
			return nil, "", nil, "", err
		}
		return srcFsh, rsrc, destFsh, rdest, nil
	}

	// doRaw / doPdf are the shared bodies behind the JS functions so that
	// converter.toJpg() can reuse them.
	doRaw := func(vsrc, vdest string) bool {
		srcFsh, rsrc, destFsh, rdest, err := resolveSrcDest(vsrc, vdest)
		if err != nil {
			g.RaiseError(err)
			return false
		}
		if err := rawFileToJpeg(srcFsh, rsrc, destFsh, rdest); err != nil {
			g.RaiseError(err)
			return false
		}
		return true
	}

	doPdf := func(vsrc, vdest string, page, dpi int) bool {
		srcFsh, rsrc, destFsh, rdest, err := resolveSrcDest(vsrc, vdest)
		if err != nil {
			g.RaiseError(err)
			return false
		}
		if err := pdfFileToJpeg(srcFsh, rsrc, destFsh, rdest, page, dpi); err != nil {
			g.RaiseError(err)
			return false
		}
		return true
	}

	// converter.rawToJpg(src, dest) -> bool
	// Converts a camera RAW file to JPEG by extracting its embedded preview.
	vm.Set("_converter_rawToJpg", func(call otto.FunctionCall) otto.Value {
		vsrc, err := call.Argument(0).ToString()
		if err != nil {
			g.RaiseError(err)
			return otto.FalseValue()
		}
		vdest, err := call.Argument(1).ToString()
		if err != nil || vdest == "" || vdest == "undefined" {
			g.RaiseError(errors.New("output filename not provided"))
			return otto.FalseValue()
		}
		if doRaw(vsrc, vdest) {
			return otto.TrueValue()
		}
		return otto.FalseValue()
	})

	// converter.pdfToJpg(src, dest, page, dpi) -> bool
	// page defaults to 1, dpi defaults to 150. page/dpi are honoured when a
	// host PDF rasterizer is available; the pure-Go fallback ignores them.
	vm.Set("_converter_pdfToJpg", func(call otto.FunctionCall) otto.Value {
		vsrc, err := call.Argument(0).ToString()
		if err != nil {
			g.RaiseError(err)
			return otto.FalseValue()
		}
		vdest, err := call.Argument(1).ToString()
		if err != nil || vdest == "" || vdest == "undefined" {
			g.RaiseError(errors.New("output filename not provided"))
			return otto.FalseValue()
		}
		page := int64(1)
		if !call.Argument(2).IsUndefined() {
			if p, e := call.Argument(2).ToInteger(); e == nil {
				page = p
			}
		}
		dpi := int64(150)
		if !call.Argument(3).IsUndefined() {
			if d, e := call.Argument(3).ToInteger(); e == nil {
				dpi = d
			}
		}
		if doPdf(vsrc, vdest, int(page), int(dpi)) {
			return otto.TrueValue()
		}
		return otto.FalseValue()
	})

	// converter.toJpg(src, dest) -> bool
	// Auto-detects the source type by extension and routes to rawToJpg / pdfToJpg.
	vm.Set("_converter_toJpg", func(call otto.FunctionCall) otto.Value {
		vsrc, err := call.Argument(0).ToString()
		if err != nil {
			g.RaiseError(err)
			return otto.FalseValue()
		}
		vdest, err := call.Argument(1).ToString()
		if err != nil || vdest == "" || vdest == "undefined" {
			g.RaiseError(errors.New("output filename not provided"))
			return otto.FalseValue()
		}

		if metadata.IsRawImageFile(vsrc) {
			if doRaw(vsrc, vdest) {
				return otto.TrueValue()
			}
			return otto.FalseValue()
		} else if isPdfFile(vsrc) {
			if doPdf(vsrc, vdest, 1, 150) {
				return otto.TrueValue()
			}
			return otto.FalseValue()
		}

		g.RaiseError(errors.New("unsupported source format for converter.toJpg: " + strings.ToLower(filepath.Ext(vsrc))))
		return otto.FalseValue()
	})

	// converter.isRawFile(path) -> bool
	vm.Set("_converter_isRawFile", func(call otto.FunctionCall) otto.Value {
		path, _ := call.Argument(0).ToString()
		result, _ := vm.ToValue(metadata.IsRawImageFile(path))
		return result
	})

	// converter.isPdfFile(path) -> bool
	vm.Set("_converter_isPdfFile", func(call otto.FunctionCall) otto.Value {
		path, _ := call.Argument(0).ToString()
		result, _ := vm.ToValue(isPdfFile(path))
		return result
	})

	// converter.pdfEngineAvailable() -> bool
	// Reports whether a host PDF rasterizer is installed. When false, PDF
	// conversion still works for image-based PDFs via the pure-Go fallback.
	vm.Set("_converter_pdfEngineAvailable", func(call otto.FunctionCall) otto.Value {
		result, _ := vm.ToValue(findPdfRasterizer() != "")
		return result
	})

	// converter.supportedRawFormats() -> [".arw", ".cr2", ...]
	vm.Set("_converter_supportedRawFormats", func(call otto.FunctionCall) otto.Value {
		result, _ := vm.ToValue(metadata.RawImageFormats)
		return result
	})

	// Wrap the native functions into a converter class
	vm.Run(`
		var converter = {};
		converter.rawToJpg = _converter_rawToJpg;
		converter.pdfToJpg = _converter_pdfToJpg;
		converter.toJpg = _converter_toJpg;
		converter.isRawFile = _converter_isRawFile;
		converter.isPdfFile = _converter_isPdfFile;
		converter.pdfEngineAvailable = _converter_pdfEngineAvailable;
		converter.supportedRawFormats = _converter_supportedRawFormats;
	`)
}

/* ────────────────────────────────────────────────────────────────────────── */
/* Conversion core (pure functions, independent of the otto VM and user object) */
/* ────────────────────────────────────────────────────────────────────────── */

// isPdfFile reports whether the given path has a .pdf extension (case-insensitive).
func isPdfFile(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".pdf"
}

// isJpegOutput reports whether the given path has a .jpg or .jpeg extension.
func isJpegOutput(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".jpg" || ext == ".jpeg"
}

// rawFileToJpeg reads a camera RAW file via srcFsh and writes its embedded
// full-resolution JPEG preview to rdest on destFsh. WriteFile (rather than a
// streamed Create) is used so this works on every filesystem type, including
// buffered remotes such as S3, FTP and WebDAV.
func rawFileToJpeg(srcFsh *filesystem.FileSystemHandler, rsrc string, destFsh *filesystem.FileSystemHandler, rdest string) error {
	if !srcFsh.FileSystemAbstraction.FileExists(rsrc) {
		return errors.New("source file not exists: " + rsrc)
	}
	if !metadata.IsRawImageFile(rsrc) {
		return errors.New("source is not a supported RAW format (" + strings.ToLower(filepath.Ext(rsrc)) +
			"); supported: " + strings.Join(metadata.RawImageFormats, ", "))
	}
	if !isJpegOutput(rdest) {
		return errors.New("destination must have a .jpg or .jpeg extension")
	}

	jpegData, err := metadata.RenderRAWImage(srcFsh, rsrc)
	if err != nil {
		return err
	}

	return destFsh.FileSystemAbstraction.WriteFile(rdest, jpegData, 0775)
}

// pdfFileToJpeg renders a single page of a PDF to JPEG and writes it to rdest on
// destFsh. It uses a host PDF rasterizer when available; otherwise it falls back
// to extracting the largest embedded JPEG (image-based / scanned PDFs).
func pdfFileToJpeg(srcFsh *filesystem.FileSystemHandler, rsrc string, destFsh *filesystem.FileSystemHandler, rdest string, page, dpi int) error {
	fsa := srcFsh.FileSystemAbstraction
	if !fsa.FileExists(rsrc) {
		return errors.New("source file not exists: " + rsrc)
	}
	if !isPdfFile(rsrc) {
		return errors.New("source is not a PDF file (" + strings.ToLower(filepath.Ext(rsrc)) + ")")
	}
	if !isJpegOutput(rdest) {
		return errors.New("destination must have a .jpg or .jpeg extension")
	}

	// Preferred path: use a host PDF rasterizer for faithful page rendering.
	if tool := findPdfRasterizer(); tool != "" {
		jpegData, err := rasterizePdfPage(srcFsh, rsrc, tool, page, dpi)
		if err == nil {
			return destFsh.FileSystemAbstraction.WriteFile(rdest, jpegData, 0775)
		}
		// The external tool failed (e.g. malformed PDF); fall through to the
		// embedded-image extraction as a best-effort fallback.
		logger.PrintAndLog("Agi", "[AGI] PDF rasterizer "+tool+" failed, falling back to embedded image extraction: "+err.Error(), nil)
	}

	// Fallback path (always available, pure Go): pull the largest embedded JPEG
	// out of the PDF. Works for scanned / image-based PDFs.
	data, err := fsa.ReadFile(rsrc)
	if err != nil {
		return errors.New("failed to read PDF: " + err.Error())
	}
	jpegData, err := metadata.ExtractLargestEmbeddedJPEG(data)
	if err != nil {
		return errors.New("no host PDF engine available and no embedded JPEG image found in PDF; " +
			"vector/text PDFs require pdftoppm, pdftocairo, mutool or ghostscript on the host")
	}
	return destFsh.FileSystemAbstraction.WriteFile(rdest, jpegData, 0775)
}

// findPdfRasterizer returns the first known PDF rasterizer tool found in PATH,
// or "" if none are installed on the host.
func findPdfRasterizer() string {
	for _, tool := range pdfRasterizers {
		if _, err := exec.LookPath(tool); err == nil {
			return tool
		}
	}
	return ""
}

// rasterizePdfPage buffers the PDF to local disk, runs the given host rasterizer
// to render a single page, and returns the resulting JPEG bytes.
func rasterizePdfPage(srcFsh *filesystem.FileSystemHandler, rsrc, tool string, page, dpi int) ([]byte, error) {
	localPdf, err := srcFsh.BufferRemoteToLocal(rsrc)
	if err != nil {
		return nil, errors.New("failed to buffer PDF to local: " + err.Error())
	}
	defer os.Remove(localPdf)

	outDir := filepath.Dir(localPdf)
	outBase := uuid.NewV4().String()
	cmdName, args, expectedOut, err := buildPdfRasterizeArgs(tool, localPdf, outDir, outBase, page, dpi)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(cmdName, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, errors.New(tool + " failed: " + err.Error() + " (" + strings.TrimSpace(string(out)) + ")")
	}

	// Locate the produced JPEG. Most tools write exactly expectedOut; some may
	// append a page-number suffix, so fall back to globbing the output dir.
	outPath := expectedOut
	if !utils.FileExists(outPath) {
		matches, _ := filepath.Glob(filepath.Join(outDir, outBase+"*.jpg"))
		if len(matches) == 0 {
			matches, _ = filepath.Glob(filepath.Join(outDir, outBase+"*.jpeg"))
		}
		if len(matches) == 0 {
			return nil, errors.New("PDF rasterizer produced no output file")
		}
		outPath = matches[0]
	}
	defer os.Remove(outPath)

	return os.ReadFile(outPath)
}

// buildPdfRasterizeArgs builds the command name, arguments and expected output
// path needed to render a single PDF page to JPEG with the given tool.
//
//	inputPath : local path of the source PDF
//	outDir    : local directory the tool should write into
//	outBase   : output filename stem (no extension)
//	page      : 1-based page number (values < 1 are clamped to 1)
//	dpi       : render resolution (values < 1 are clamped to 150)
func buildPdfRasterizeArgs(tool, inputPath, outDir, outBase string, page, dpi int) (string, []string, string, error) {
	if page < 1 {
		page = 1
	}
	if dpi < 1 {
		dpi = 150
	}
	p := strconv.Itoa(page)
	r := strconv.Itoa(dpi)

	switch tool {
	case "pdftoppm", "pdftocairo":
		// -singlefile makes the output exactly <outBase>.jpg
		outPrefix := filepath.Join(outDir, outBase)
		return tool, []string{"-jpeg", "-r", r, "-f", p, "-l", p, "-singlefile", inputPath, outPrefix}, outPrefix + ".jpg", nil
	case "gs":
		outPath := filepath.Join(outDir, outBase+".jpg")
		return "gs", []string{"-dNOPAUSE", "-dBATCH", "-dSAFER", "-q",
			"-sDEVICE=jpeg", "-r" + r, "-dFirstPage=" + p, "-dLastPage=" + p,
			"-sOutputFile=" + outPath, inputPath}, outPath, nil
	case "mutool":
		// mutool draw -o out.jpg -r <dpi> input.pdf <page>
		outPath := filepath.Join(outDir, outBase+".jpg")
		return "mutool", []string{"draw", "-o", outPath, "-r", r, inputPath, p}, outPath, nil
	default:
		return "", nil, "", errors.New("unsupported PDF rasterizer: " + tool)
	}
}
