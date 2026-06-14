package agi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robertkrimen/otto"

	"imuslab.com/arozos/mod/agi/static"
	user "imuslab.com/arozos/mod/user"
	"imuslab.com/arozos/mod/utils"
)

/*
	AJGI Image Recognition Library

	This library lets AGI scripts run AI photo recognition — image tagging and
	face recognition — by delegating to the "imagerecognition" ArozOS
	subservice (see subservices/imagerecognition). The subservice performs the
	heavy lifting (object/scene tagging and grouping the same person across
	photos under a stable UUID); this library reads an image from the user's
	virtual filesystem, forwards it to the subservice and returns the result.

	The connection is configured in System Settings > AI Integration >
	Photo Recognition, which auto-detects the subservice when it is installed.

	Load in a script with:  requirelib("imagerecognition");
*/

const (
	//imageRecognitionDBTable persists the connection configuration.
	imageRecognitionDBTable = "imagerecognition"

	//imageRecognitionTimeout bounds a single recognition request.
	imageRecognitionTimeout = 60 * time.Second

	//imageRecognitionMaxBytes guards against forwarding an absurdly large file.
	imageRecognitionMaxBytes = 64 << 20 //64 MiB
)

// ImageRecognitionConfig is the admin-configured connection to the recognizer.
type ImageRecognitionConfig struct {
	Endpoint string `json:"endpoint"` //Base URL of the subservice, e.g. http://localhost:12810
	Enabled  bool   `json:"enabled"`  //Master switch; AGI access is refused when false
	Manual   bool   `json:"manual"`   //True once an admin sets the endpoint by hand (disables auto-detect)
}

// ImageRecognitionLibRegister registers the "imagerecognition" library and
// ensures its storage table exists.
func (g *Gateway) ImageRecognitionLibRegister() {
	sysdb := g.Option.UserHandler.GetDatabase()
	if !sysdb.TableExists(imageRecognitionDBTable) {
		sysdb.NewTable(imageRecognitionDBTable)
	}

	err := g.RegisterLib("imagerecognition", g.injectImageRecognitionFunctions)
	if err != nil {
		agiLogger.PrintAndLog("Agi", fmt.Sprint(err), nil)
		os.Exit(1)
	}
}

func (g *Gateway) injectImageRecognitionFunctions(payload *static.AgiLibInjectionPayload) {
	vm := payload.VM
	u := payload.User

	//imagerecognition.ready() => bool
	vm.Set("_imgrecog_ready", func(call otto.FunctionCall) otto.Value {
		cfg := g.getImageRecognitionConfig()
		ready := cfg.Enabled && strings.TrimSpace(cfg.Endpoint) != ""
		v, _ := vm.ToValue(ready)
		return v
	})

	//Generic image submission: reads vpath, POSTs to apiPath, returns JSON text.
	vm.Set("_imgrecog_call", func(call otto.FunctionCall) otto.Value {
		apiPath, _ := call.Argument(0).ToString()
		vpath, _ := call.Argument(1).ToString()

		body, err := g.imageRecognitionSubmit(u, apiPath, vpath)
		if err != nil {
			panic(vm.MakeCustomError("ImageRecognitionError", err.Error()))
		}
		v, _ := vm.ToValue(string(body))
		return v
	})

	//imagerecognition.listPeople() => JSON text of the known people gallery.
	vm.Set("_imgrecog_listPeople", func(call otto.FunctionCall) otto.Value {
		body, err := g.imageRecognitionGet("/api/face/people")
		if err != nil {
			panic(vm.MakeCustomError("ImageRecognitionError", err.Error()))
		}
		v, _ := vm.ToValue(string(body))
		return v
	})

	//Wrap the native calls in a friendly object.
	vm.Run(`
		var imagerecognition = {};
		imagerecognition.ready = function(){
			return _imgrecog_ready();
		};
		imagerecognition.tag = function(vpath){
			return JSON.parse(_imgrecog_call("/api/tag", vpath)).tags;
		};
		imagerecognition.detectFaces = function(vpath){
			return JSON.parse(_imgrecog_call("/api/face/detect", vpath)).faces;
		};
		imagerecognition.recognizeFaces = function(vpath){
			return JSON.parse(_imgrecog_call("/api/face/recognize", vpath)).faces;
		};
		imagerecognition.analyze = function(vpath){
			return JSON.parse(_imgrecog_call("/api/analyze", vpath));
		};
		imagerecognition.listPeople = function(){
			return JSON.parse(_imgrecog_listPeople()).people;
		};
	`)
}

// imageRecognitionSubmit reads the image at vpath from the user's filesystem
// and forwards it to the subservice endpoint apiPath as a multipart upload.
func (g *Gateway) imageRecognitionSubmit(u *user.User, apiPath string, vpath string) ([]byte, error) {
	cfg := g.getImageRecognitionConfig()
	if !cfg.Enabled || strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("photo recognition is not enabled (System Settings > AI Integration > Photo Recognition)")
	}

	//Permission + read from the (possibly remote) virtual filesystem.
	if !u.CanRead(vpath) {
		return nil, errors.New("permission denied reading " + vpath)
	}
	fsh, rpath, err := static.VirtualPathToRealPath(vpath, u)
	if err != nil {
		return nil, err
	}
	if !fsh.FileSystemAbstraction.FileExists(rpath) {
		return nil, errors.New("file not found: " + vpath)
	}
	imageBytes, err := fsh.FileSystemAbstraction.ReadFile(rpath)
	if err != nil {
		return nil, errors.New("could not read image: " + err.Error())
	}
	if len(imageBytes) > imageRecognitionMaxBytes {
		return nil, errors.New("image is too large for recognition")
	}

	//Build the multipart body.
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("image", filepath.Base(rpath))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(imageBytes); err != nil {
		return nil, err
	}
	writer.Close()

	url := strings.TrimRight(cfg.Endpoint, "/") + apiPath
	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	return imageRecognitionDo(req)
}

// imageRecognitionGet performs a GET against the subservice and returns the body.
func (g *Gateway) imageRecognitionGet(apiPath string) ([]byte, error) {
	cfg := g.getImageRecognitionConfig()
	if !cfg.Enabled || strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("photo recognition is not enabled")
	}
	req, err := http.NewRequest("GET", strings.TrimRight(cfg.Endpoint, "/")+apiPath, nil)
	if err != nil {
		return nil, err
	}
	return imageRecognitionDo(req)
}

// imageRecognitionDo executes req against the subservice and validates the
// response, surfacing the service's error message on a non-200 status.
func imageRecognitionDo(req *http.Request) ([]byte, error) {
	client := &http.Client{Timeout: imageRecognitionTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("could not reach photo recognition service: " + err.Error())
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, imageRecognitionMaxBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if parsed := extractServiceError(body); parsed != "" {
			msg = parsed
		}
		return nil, fmt.Errorf("photo recognition service error (%d): %s", resp.StatusCode, msg)
	}
	return body, nil
}

// extractServiceError pulls the "message" field out of the subservice's JSON
// error envelope, if present.
func extractServiceError(body []byte) string {
	var env struct {
		Error   bool   `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Message != "" {
		return env.Message
	}
	return ""
}

// ── Configuration storage ────────────────────────────────────────────────────

// getImageRecognitionConfig loads the stored configuration (zero value when
// unset).
func (g *Gateway) getImageRecognitionConfig() ImageRecognitionConfig {
	cfg := ImageRecognitionConfig{}
	sysdb := g.Option.UserHandler.GetDatabase()
	if sysdb.KeyExists(imageRecognitionDBTable, "config") {
		sysdb.Read(imageRecognitionDBTable, "config", &cfg)
	}
	return cfg
}

// GetImageRecognitionConfig exposes the stored configuration to the settings
// layer (package main).
func (g *Gateway) GetImageRecognitionConfig() ImageRecognitionConfig {
	return g.getImageRecognitionConfig()
}

// SaveImageRecognitionConfig persists the configuration.
func (g *Gateway) SaveImageRecognitionConfig(cfg ImageRecognitionConfig) error {
	sysdb := g.Option.UserHandler.GetDatabase()
	return sysdb.Write(imageRecognitionDBTable, "config", cfg)
}

// ApplyDetectedImageRecognitionEndpoint is called by the settings layer when it
// discovers (or loses) the installed subservice. It updates the stored endpoint
// and enabled flag, unless an admin has taken manual control of the connection.
func (g *Gateway) ApplyDetectedImageRecognitionEndpoint(endpoint string, installed bool) {
	cfg := g.getImageRecognitionConfig()
	if cfg.Manual {
		return //Respect an explicit admin configuration.
	}
	changed := false
	if installed {
		if cfg.Endpoint != endpoint || !cfg.Enabled {
			cfg.Endpoint = endpoint
			cfg.Enabled = true
			changed = true
		}
	} else {
		//Subservice not installed: disable auto access but keep the endpoint hint.
		if cfg.Enabled {
			cfg.Enabled = false
			changed = true
		}
	}
	if changed {
		if err := g.SaveImageRecognitionConfig(cfg); err != nil {
			agiLogger.PrintAndLog("Agi", "failed to persist image recognition config: "+err.Error(), err)
		}
	}
}

// ── Settings endpoint handlers ───────────────────────────────────────────────

// HandleImageRecognitionConfig serves GET (current config) and POST (manual
// override) of the connection settings. Admin-gated by the caller's router.
//
//	GET  /system/imagerecognition/config
//	POST /system/imagerecognition/config  (endpoint, enabled, auto)
func (g *Gateway) HandleImageRecognitionConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfg := g.getImageRecognitionConfig()
		js, _ := json.Marshal(cfg)
		utils.SendJSONResponse(w, string(js))
		return
	}

	//POST. An "auto" request clears the manual override so detection resumes.
	if auto, _ := utils.PostBool(r, "auto"); auto {
		cfg := g.getImageRecognitionConfig()
		cfg.Manual = false
		if err := g.SaveImageRecognitionConfig(cfg); err != nil {
			utils.SendErrorResponse(w, "failed to save config: "+err.Error())
			return
		}
		utils.SendOK(w)
		return
	}

	endpoint, err := utils.PostPara(r, "endpoint")
	if err != nil {
		utils.SendErrorResponse(w, "missing endpoint")
		return
	}
	enabled, _ := utils.PostBool(r, "enabled")

	cfg := g.getImageRecognitionConfig()
	cfg.Endpoint = strings.TrimSpace(endpoint)
	cfg.Enabled = enabled
	cfg.Manual = true //Admin took control.
	if err := g.SaveImageRecognitionConfig(cfg); err != nil {
		utils.SendErrorResponse(w, "failed to save config: "+err.Error())
		return
	}
	utils.SendOK(w)
}

// HandleImageRecognitionTest probes the configured subservice and returns its
// /api/info payload so the settings page can confirm connectivity.
//
//	POST /system/imagerecognition/test   (optional: endpoint)
func (g *Gateway) HandleImageRecognitionTest(w http.ResponseWriter, r *http.Request) {
	endpoint := strings.TrimSpace(r.FormValue("endpoint"))
	if endpoint == "" {
		endpoint = g.getImageRecognitionConfig().Endpoint
	}
	if endpoint == "" {
		utils.SendErrorResponse(w, "no endpoint configured")
		return
	}

	req, err := http.NewRequest("GET", strings.TrimRight(endpoint, "/")+"/api/info", nil)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	body, err := imageRecognitionDo(req)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	utils.SendJSONResponse(w, string(body))
}
