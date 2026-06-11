package agi

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/robertkrimen/otto"

	"imuslab.com/arozos/mod/agi/static"
	"imuslab.com/arozos/mod/filesystem"
	"imuslab.com/arozos/mod/filesystem/metadata"
	"imuslab.com/arozos/mod/info/logger"
)

/*
	AGI File Converter Library

	This library converts camera RAW photos that browsers cannot display
	directly into plain JPEG images, all through the arozos virtualized file
	system layer.

	RAW conversion (ARW / CR2 / DNG / NEF / RAF / ORF) is performed purely in
	Go by extracting the embedded full-resolution JPEG preview, so it is always
	available with no external dependencies.

	Author: tobychui
*/

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

		srcFsh, rsrc, destFsh, rdest, err := resolveSrcDest(vsrc, vdest)
		if err != nil {
			g.RaiseError(err)
			return otto.FalseValue()
		}
		if err := rawFileToJpeg(srcFsh, rsrc, destFsh, rdest); err != nil {
			g.RaiseError(err)
			return otto.FalseValue()
		}
		return otto.TrueValue()
	})

	// converter.isRawFile(path) -> bool
	vm.Set("_converter_isRawFile", func(call otto.FunctionCall) otto.Value {
		path, _ := call.Argument(0).ToString()
		result, _ := vm.ToValue(metadata.IsRawImageFile(path))
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
		converter.isRawFile = _converter_isRawFile;
		converter.supportedRawFormats = _converter_supportedRawFormats;
	`)
}

/* ────────────────────────────────────────────────────────────────────────── */
/* Conversion core (pure functions, independent of the otto VM and user object) */
/* ────────────────────────────────────────────────────────────────────────── */

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
