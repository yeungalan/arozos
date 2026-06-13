package facerecognition

/*
	handlers.go

	HTTP handlers for the face recognition feature.

	Admin endpoints (wired behind an AdminOnly permission router):
	  HandleConfig    GET/POST the system wide configuration (on/off switch)
	  HandleClearAll  wipe the stored face data of every user

	User endpoints (wired behind the Photo module permission router):
	  HandleStatus       feature switch + per-user statistics
	  HandleScan         scan a batch of photos for faces
	  HandlePeople       list the people clusters of the user
	  HandleRenamePerson set the display name of a person
	  HandlePersonPhotos list the photos containing a person
	  HandlePhotoFaces   list the faces stored for one photo
	  HandleClearUser    wipe the face data of the requesting user
*/

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	fs "imuslab.com/arozos/mod/filesystem"
	user "imuslab.com/arozos/mod/user"
	"imuslab.com/arozos/mod/utils"
)

// HandleConfig reads (GET) or updates (POST) the system wide configuration.
// Must be registered admin-only: this is the on/off switch shown in
// System Settings > AI Integration > Face Recognition.
func (m *Manager) HandleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		js, _ := json.Marshal(m.GetConfig())
		utils.SendJSONResponse(w, string(js))
		return
	}

	cfg := m.GetConfig()
	if enabled, err := utils.PostBool(r, "enabled"); err == nil {
		cfg.Enabled = enabled
	}
	if engine, err := utils.PostPara(r, "engine"); err == nil {
		cfg.Engine = engine
	}
	if minFaceSize, err := utils.PostInt(r, "minFaceSize"); err == nil {
		cfg.MinFaceSize = minFaceSize
	}
	if thresholdString, err := utils.PostPara(r, "matchThreshold"); err == nil {
		if threshold, err := strconv.ParseFloat(thresholdString, 64); err == nil {
			cfg.MatchThreshold = threshold
		}
	}
	if onnxLibPath, err := utils.PostPara(r, "onnxLibPath"); err == nil {
		cfg.OnnxLibPath = strings.TrimSpace(onnxLibPath)
	}
	if modelPath, err := utils.PostPara(r, "modelPath"); err == nil {
		cfg.ModelPath = strings.TrimSpace(modelPath)
	}
	if inputSize, err := utils.PostInt(r, "inputSize"); err == nil {
		cfg.InputSize = inputSize
	}
	if thresholdString, err := utils.PostPara(r, "onnxMatchThreshold"); err == nil {
		if threshold, err := strconv.ParseFloat(thresholdString, 64); err == nil {
			cfg.OnnxMatchThreshold = threshold
		}
	}

	if err := m.SetConfig(cfg); err != nil {
		utils.SendErrorResponse(w, "unable to save configuration: "+err.Error())
		return
	}
	utils.SendOK(w)
}

// HandleModelTest validates the deep-engine configuration by loading the ONNX
// Runtime library and the model and reporting the embedding dimension. Admin
// only; this is the "Test model" button in System Settings.
func (m *Manager) HandleModelTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.SendErrorResponse(w, "invalid request method")
		return
	}
	libPath, _ := utils.PostPara(r, "onnxLibPath")
	modelPath, _ := utils.PostPara(r, "modelPath")
	inputSize, err := utils.PostInt(r, "inputSize")
	if err != nil {
		inputSize = defaultModelInputSize
	}
	modelPath = strings.TrimSpace(modelPath)
	if modelPath == "" {
		utils.SendErrorResponse(w, "model path is required")
		return
	}

	engine, err := newONNXEngine(strings.TrimSpace(libPath), modelPath, inputSize)
	if err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	dimension := engine.Dimension()
	engine.Close()

	js, _ := json.Marshal(map[string]interface{}{
		"ok":        true,
		"dimension": dimension,
	})
	utils.SendJSONResponse(w, string(js))
}

// HandleClearAll removes the stored face data of every user. Admin only.
func (m *Manager) HandleClearAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.SendErrorResponse(w, "invalid request method")
		return
	}
	if err := m.ClearAllData(); err != nil {
		utils.SendErrorResponse(w, "unable to clear face data: "+err.Error())
		return
	}
	utils.SendOK(w)
}

// HandleStatus returns the feature switch and, when enabled, the statistics
// of the requesting user. The Photo app uses this to decide whether to show
// any face recognition UI at all.
func (m *Manager) HandleStatus(w http.ResponseWriter, r *http.Request) {
	userinfo, err := m.options.UserHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not logged in")
		return
	}

	cfg := m.GetConfig()
	response := map[string]interface{}{
		"enabled": cfg.Enabled,
		"engine":  cfg.Engine,
	}
	if cfg.Enabled {
		//Report which engine is actually active (deep falls back to classical
		//when the model/library cannot be loaded).
		response["deepActive"] = cfg.Engine == EngineONNX && m.getONNXEngine(cfg) != nil
		stats := m.GetUserStats(userinfo.Username)
		response["scannedPhotos"] = stats.ScannedPhotos
		response["totalFaces"] = stats.TotalFaces
		response["people"] = stats.People
	}
	js, _ := json.Marshal(response)
	utils.SendJSONResponse(w, string(js))
}

// scanRequest is the JSON body of a HandleScan call
type scanRequest struct {
	Paths []string `json:"paths"`
}

// HandleScan scans a batch of photos (by virtual path) for faces. Photos
// already scanned with unchanged size/modtime are skipped, so the Photo app
// can keep re-submitting its whole index cheaply.
func (m *Manager) HandleScan(w http.ResponseWriter, r *http.Request) {
	userinfo, err := m.options.UserHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not logged in")
		return
	}
	if r.Method != http.MethodPost {
		utils.SendErrorResponse(w, "invalid request method")
		return
	}

	cfg := m.GetConfig()
	if !cfg.Enabled {
		utils.SendErrorResponse(w, "face recognition is disabled")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		utils.SendErrorResponse(w, "unable to read request")
		return
	}
	request := scanRequest{}
	if err := json.Unmarshal(body, &request); err != nil {
		utils.SendErrorResponse(w, "invalid request payload")
		return
	}
	if len(request.Paths) > maxPathsPerScan {
		request.Paths = request.Paths[:maxPathsPerScan]
	}

	//Resolve the active engine and wipe stored data if the engine/model
	//changed since the last scan, so descriptors are never mixed.
	mt := m.currentMatcher(cfg)
	m.ensureSignature(mt.signature)

	//Serialize scans of the same user so people clusters stay consistent
	lock := m.userLock(userinfo.Username)
	lock.Lock()
	defer lock.Unlock()

	processed := 0
	skipped := 0
	removed := 0
	failed := 0
	facesFound := 0
	for _, vpath := range request.Paths {
		result, err := m.scanSinglePhoto(userinfo, vpath, cfg, mt)
		if err != nil {
			failed++
			continue
		}
		switch result.outcome {
		case scanOutcomeProcessed:
			processed++
			facesFound += result.faces
		case scanOutcomeSkipped:
			skipped++
		case scanOutcomeRemoved:
			removed++
		}
	}

	js, _ := json.Marshal(map[string]interface{}{
		"processed": processed,
		"skipped":   skipped,
		"removed":   removed,
		"failed":    failed,
		"faces":     facesFound,
		"people":    len(m.ListPeople(userinfo.Username)),
	})
	utils.SendJSONResponse(w, string(js))
}

const (
	scanOutcomeProcessed = iota
	scanOutcomeSkipped
	scanOutcomeRemoved
)

type scanResult struct {
	outcome int
	faces   int
}

// scanSinglePhoto resolves, loads and scans one photo of a user using the
// given matcher (which carries the active engine). Caller must hold the user
// scan lock.
func (m *Manager) scanSinglePhoto(userinfo *user.User, vpath string, cfg Config, mt matcher) (*scanResult, error) {
	if vpath == "" || !userinfo.CanRead(vpath) {
		return nil, errors.New("access denied")
	}
	if !supportedImageExt(filepath.Ext(vpath)) {
		return nil, errors.New("unsupported file format")
	}

	fsh, err := userinfo.GetFileSystemHandlerFromVirtualPath(vpath)
	if err != nil {
		return nil, err
	}
	_, subpath, err := fs.GetIDFromVirtualPath(vpath)
	if err != nil {
		return nil, err
	}
	fshAbs := fsh.FileSystemAbstraction
	rpath, err := fshAbs.VirtualPathToRealPath(subpath, userinfo.Username)
	if err != nil {
		return nil, err
	}

	if !fshAbs.FileExists(rpath) {
		//The photo is gone: forget its faces
		m.RemovePhoto(userinfo.Username, vpath)
		return &scanResult{outcome: scanOutcomeRemoved}, nil
	}

	filesize := fshAbs.GetFileSize(rpath)
	if filesize > maxImageFileSize {
		return nil, errors.New("image file too large")
	}
	modtime, _ := fshAbs.GetModTime(rpath)

	if !m.NeedsScan(userinfo.Username, vpath, filesize, modtime) {
		return &scanResult{outcome: scanOutcomeSkipped}, nil
	}

	imageBytes, err := fshAbs.ReadFile(rpath)
	if err != nil {
		return nil, err
	}
	img, err := DecodeImage(imageBytes, filepath.Ext(vpath))
	if err != nil {
		return nil, err
	}

	faces, err := m.detector.DetectFaces(img, cfg.MinFaceSize)
	if err != nil {
		return nil, err
	}

	//When the deep engine is active, replace the classical descriptor of each
	//face with a model embedding computed from a higher-resolution crop of the
	//original image. Faces whose embedding fails are dropped rather than mixed.
	if mt.engine != nil {
		embedded := faces[:0]
		for _, face := range faces {
			crop := cropFace(img, face.X, face.Y, face.W, face.H, faceCropMargin)
			embedding, err := mt.engine.Embed(crop)
			if err != nil {
				continue
			}
			face.descriptor = embedding
			embedded = append(embedded, face)
		}
		faces = embedded
	}

	entry := &PhotoFaces{
		VPath:     vpath,
		FileSize:  filesize,
		ModTime:   modtime,
		ScannedAt: time.Now().Unix(),
	}
	if err := m.StorePhotoFaces(userinfo.Username, entry, faces, mt); err != nil {
		return nil, err
	}
	return &scanResult{outcome: scanOutcomeProcessed, faces: len(faces)}, nil
}

// HandlePeople lists the people clusters of the requesting user
func (m *Manager) HandlePeople(w http.ResponseWriter, r *http.Request) {
	userinfo, err := m.options.UserHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not logged in")
		return
	}
	if !m.Enabled() {
		utils.SendErrorResponse(w, "face recognition is disabled")
		return
	}

	people := m.ListPeople(userinfo.Username)
	results := []map[string]interface{}{}
	for _, person := range people {
		results = append(results, map[string]interface{}{
			"id":    person.ID,
			"name":  person.DisplayName(),
			"named": person.Name != "",
			"count": person.FaceCount,
			"thumb": person.Thumb,
		})
	}
	js, _ := json.Marshal(results)
	utils.SendJSONResponse(w, string(js))
}

// HandleRenamePerson sets the display name of a person of the requesting user
func (m *Manager) HandleRenamePerson(w http.ResponseWriter, r *http.Request) {
	userinfo, err := m.options.UserHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not logged in")
		return
	}
	if !m.Enabled() {
		utils.SendErrorResponse(w, "face recognition is disabled")
		return
	}

	personID, err := utils.PostPara(r, "id")
	if err != nil {
		utils.SendErrorResponse(w, "invalid person id")
		return
	}
	name, _ := utils.PostPara(r, "name")
	if err := m.RenamePerson(userinfo.Username, personID, name); err != nil {
		utils.SendErrorResponse(w, err.Error())
		return
	}
	utils.SendOK(w)
}

// HandlePersonPhotos lists the photos of the requesting user that contain
// the given person. The response rows match the Photo grid data shape.
func (m *Manager) HandlePersonPhotos(w http.ResponseWriter, r *http.Request) {
	userinfo, err := m.options.UserHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not logged in")
		return
	}
	if !m.Enabled() {
		utils.SendErrorResponse(w, "face recognition is disabled")
		return
	}

	personID, err := utils.GetPara(r, "id")
	if err != nil {
		utils.SendErrorResponse(w, "invalid person id")
		return
	}
	person := m.GetPerson(userinfo.Username, personID)
	if person == nil {
		utils.SendErrorResponse(w, "person not found")
		return
	}

	photos := m.ListPersonPhotos(userinfo.Username, personID)
	results := []map[string]interface{}{}
	for _, photo := range photos {
		results = append(results, map[string]interface{}{
			"filepath": photo.VPath,
			"filesize": photo.FileSize,
		})
	}
	js, _ := json.Marshal(map[string]interface{}{
		"id":      person.ID,
		"name":    person.DisplayName(),
		"results": results,
	})
	utils.SendJSONResponse(w, string(js))
}

// HandlePhotoFaces lists the faces stored for one photo of the requesting
// user, with resolved person names. Used by the Photo viewer info panel.
func (m *Manager) HandlePhotoFaces(w http.ResponseWriter, r *http.Request) {
	userinfo, err := m.options.UserHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not logged in")
		return
	}
	if !m.Enabled() {
		utils.SendErrorResponse(w, "face recognition is disabled")
		return
	}

	vpath, err := utils.GetPara(r, "path")
	if err != nil {
		utils.SendErrorResponse(w, "invalid path")
		return
	}

	results := []map[string]interface{}{}
	if entry := m.GetPhotoFaces(userinfo.Username, vpath); entry != nil {
		for _, face := range entry.Faces {
			name := "Person " + face.PersonID
			if person := m.GetPerson(userinfo.Username, face.PersonID); person != nil {
				name = person.DisplayName()
			}
			results = append(results, map[string]interface{}{
				"personId": face.PersonID,
				"name":     name,
				"x":        face.X,
				"y":        face.Y,
				"w":        face.W,
				"h":        face.H,
				"thumb":    face.Thumb,
			})
		}
	}
	js, _ := json.Marshal(map[string]interface{}{
		"faces": results,
	})
	utils.SendJSONResponse(w, string(js))
}

// HandleClearUser removes every stored face and person of the requesting
// user. This is the per-user privacy escape hatch.
func (m *Manager) HandleClearUser(w http.ResponseWriter, r *http.Request) {
	userinfo, err := m.options.UserHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "user not logged in")
		return
	}
	if r.Method != http.MethodPost {
		utils.SendErrorResponse(w, "invalid request method")
		return
	}

	lock := m.userLock(userinfo.Username)
	lock.Lock()
	defer lock.Unlock()

	if err := m.ClearUserData(userinfo.Username); err != nil {
		utils.SendErrorResponse(w, "unable to clear face data: "+err.Error())
		return
	}
	utils.SendOK(w)
}
