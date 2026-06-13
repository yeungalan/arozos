package facerecognition

/*
	Face Recognition Module for ArozOS Photo

	This module provides fully local, on-device face detection and grouping
	("people") for the Photo web app. Detection is done with the pure Go
	pigo cascade classifier (MIT licensed, no system dependencies) and the
	detected faces are grouped into people using a classic LBP (Local Binary
	Pattern) histogram descriptor. No image ever leaves the host.

	The feature is OPTIONAL and disabled by default. Administrators can turn
	it on or off from System Settings > AI Integration > Face Recognition.
	All scanning happens on the backend; the Photo app only submits virtual
	paths of indexed photos and renders the results.
*/

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"imuslab.com/arozos/mod/database"
	"imuslab.com/arozos/mod/info/logger"
	user "imuslab.com/arozos/mod/user"
)

const (
	configTable = "facerecog"        //Table for the system wide configuration
	facesTable  = "facerecog_faces"  //Table for per-photo face entries, key: username/sha256(vpath)
	peopleTable = "facerecog_people" //Table for per-user people clusters, key: username/personID

	configKey    = "config"
	signatureKey = "signature" //Identifies the engine+model the stored data was built with

	//Hard limits to keep a single request bounded
	maxPathsPerScan  = 32       //Maximum number of photos per scan request
	maxFacesPerPhoto = 16       //Maximum number of faces stored for one photo
	maxImageFileSize = 25 << 20 //25MB, same limit as the system thumbnail renderer

	//Bounds for the deep (ONNX) model input size
	minModelInputSize     = 16
	maxModelInputSize     = 1024
	defaultModelInputSize = 112 //ArcFace standard
)

// Config is the system-wide face recognition configuration, managed by
// administrators from the AI Integration tab in System Settings.
type Config struct {
	Enabled bool   `json:"enabled"` //Master switch for the whole feature
	Engine  string `json:"engine"`  //"classical" (built-in) or "onnx" (deep model)

	//Classical engine tuning
	MinFaceSize    int     `json:"minFaceSize"`    //Minimum face size in px (in the detection-scaled image)
	MatchThreshold float64 `json:"matchThreshold"` //Max classical descriptor distance for the same person

	//Deep (ONNX) engine settings. The model is supplied by the administrator;
	//ArozOS never bundles model weights.
	OnnxLibPath        string  `json:"onnxLibPath"`        //Path to the ONNX Runtime shared library (blank = auto-discover)
	ModelPath          string  `json:"modelPath"`          //Path to the face-embedding .onnx model
	InputSize          int     `json:"inputSize"`          //Model input size (square), default 112
	OnnxMatchThreshold float64 `json:"onnxMatchThreshold"` //Max cosine distance for the same person
}

// Options for creating a new face recognition manager
type Options struct {
	UserHandler *user.UserHandler  //System user handler, used to resolve users and virtual paths
	Database    *database.Database //System database for config and face storage
}

// Manager is the face recognition service instance
type Manager struct {
	options   *Options
	detector  *detector
	userlocks sync.Map //username -> *sync.Mutex, serializes scans per user

	engineMu   sync.Mutex //guards the deep engine lifecycle
	onnxEngine faceEngine //active deep engine, nil when classical/unavailable
	onnxKey    string     //config fingerprint the deep engine was built for

	signatureMu sync.Mutex //serializes signature migration (global data wipe)
}

// DefaultConfig returns the configuration used when nothing has been saved yet.
// The feature ships disabled so it stays strictly opt-in.
func DefaultConfig() Config {
	return Config{
		Enabled:            false,
		Engine:             EngineClassical,
		MinFaceSize:        60,
		MatchThreshold:     0.42,
		InputSize:          defaultModelInputSize,
		OnnxMatchThreshold: 0.65,
	}
}

// NewManager creates a new face recognition manager and prepares its
// database tables. The embedded detection cascade is unpacked lazily on
// first use so a disabled feature costs no memory.
func NewManager(options *Options) (*Manager, error) {
	if options == nil || options.Database == nil {
		return nil, errors.New("face recognition manager requires a database")
	}

	for _, table := range []string{configTable, facesTable, peopleTable} {
		if err := options.Database.NewTable(table); err != nil {
			return nil, err
		}
	}

	manager := &Manager{
		options:  options,
		detector: newDetector(),
	}

	return manager, nil
}

// classicalSignature is the data signature of the built-in LBP+tone engine.
func classicalSignature() string {
	return "classical-v" + strconv.Itoa(descriptorVersion)
}

// onnxFingerprint is a fingerprint of the deep-engine configuration, including
// the model file's size and modification time, so swapping the model or
// changing the input size rebuilds the engine and re-scans the library.
func onnxFingerprint(cfg Config) string {
	fp := cfg.OnnxLibPath + "|" + cfg.ModelPath + "|" + strconv.Itoa(cfg.InputSize)
	if fi, err := os.Stat(cfg.ModelPath); err == nil {
		fp += "|" + strconv.FormatInt(fi.Size(), 10) + "|" + strconv.FormatInt(fi.ModTime().Unix(), 10)
	}
	return fp
}

// getONNXEngine returns the deep engine for the given config, (re)building it
// when the configuration fingerprint changes. Returns nil when the deep engine
// is not selected or cannot be loaded (the caller then uses the classical
// engine). Loading failures are logged once per configuration change.
func (m *Manager) getONNXEngine(cfg Config) faceEngine {
	if cfg.Engine != EngineONNX {
		return nil
	}
	fingerprint := onnxFingerprint(cfg)

	m.engineMu.Lock()
	defer m.engineMu.Unlock()

	if m.onnxEngine != nil && m.onnxKey == fingerprint {
		return m.onnxEngine
	}
	//Configuration changed (or first use) — tear down any previous engine.
	if m.onnxEngine != nil {
		m.onnxEngine.Close()
		m.onnxEngine = nil
	}
	m.onnxKey = fingerprint

	if cfg.ModelPath == "" {
		return nil
	}
	engine, err := newONNXEngine(cfg.OnnxLibPath, cfg.ModelPath, cfg.InputSize)
	if err != nil {
		logger.PrintAndLog("FaceRecognition", "deep engine unavailable, falling back to classical", err)
		return nil
	}
	m.onnxEngine = engine
	return engine
}

// currentMatcher returns the comparison strategy and data signature for the
// engine that is actually usable under the given configuration.
func (m *Manager) currentMatcher(cfg Config) matcher {
	if engine := m.getONNXEngine(cfg); engine != nil {
		return matcher{
			distance:  cosineDistance,
			threshold: cfg.OnnxMatchThreshold,
			cosine:    true,
			signature: fmt.Sprintf("onnx-d%d-%s", engine.Dimension(), onnxFingerprint(cfg)),
			engine:    engine,
		}
	}
	return matcher{
		distance:  DescriptorDistance,
		threshold: cfg.MatchThreshold,
		cosine:    false,
		signature: classicalSignature(),
		engine:    nil,
	}
}

// ensureSignature wipes all stored faces and people when the active engine /
// model differs from the data already on disk, then records the new signature.
// The Photo app re-scans automatically afterwards. Configuration is preserved.
func (m *Manager) ensureSignature(signature string) {
	m.signatureMu.Lock()
	defer m.signatureMu.Unlock()

	stored := ""
	if m.options.Database.KeyExists(configTable, signatureKey) {
		m.options.Database.Read(configTable, signatureKey, &stored)
	}
	if stored == signature {
		return
	}
	m.ClearAllData()
	m.options.Database.Write(configTable, signatureKey, signature)
}

// GetConfig returns the stored configuration, falling back to defaults for
// missing or out-of-range values.
func (m *Manager) GetConfig() Config {
	cfg := DefaultConfig()
	if m.options.Database.KeyExists(configTable, configKey) {
		m.options.Database.Read(configTable, configKey, &cfg)
	}
	return sanitizeConfig(cfg)
}

// SetConfig validates and persists a new configuration
func (m *Manager) SetConfig(cfg Config) error {
	cfg = sanitizeConfig(cfg)
	return m.options.Database.Write(configTable, configKey, cfg)
}

// Enabled reports whether the face recognition feature is switched on
func (m *Manager) Enabled() bool {
	return m.GetConfig().Enabled
}

// sanitizeConfig clamps every tunable into its supported range so a corrupt
// or hand-edited database entry can never break the scanner.
func sanitizeConfig(cfg Config) Config {
	if cfg.Engine != EngineONNX {
		cfg.Engine = EngineClassical
	}
	if cfg.MinFaceSize < 20 {
		cfg.MinFaceSize = 20
	}
	if cfg.MinFaceSize > 500 {
		cfg.MinFaceSize = 500
	}
	if cfg.MatchThreshold <= 0 {
		cfg.MatchThreshold = DefaultConfig().MatchThreshold
	}
	if cfg.MatchThreshold > 1 {
		cfg.MatchThreshold = 1
	}

	//Deep engine bounds
	if cfg.InputSize < minModelInputSize || cfg.InputSize > maxModelInputSize {
		cfg.InputSize = defaultModelInputSize
	}
	if cfg.OnnxMatchThreshold <= 0 {
		cfg.OnnxMatchThreshold = DefaultConfig().OnnxMatchThreshold
	}
	if cfg.OnnxMatchThreshold > 2 {
		cfg.OnnxMatchThreshold = 2
	}
	return cfg
}

// userLock returns the scan mutex of the given user
func (m *Manager) userLock(username string) *sync.Mutex {
	lock, _ := m.userlocks.LoadOrStore(username, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// supportedImageExt reports whether the given file extension (with or
// without leading dot) can be decoded by the backend scanner. RAW formats
// are excluded as they require the camera vendor specific decoders.
func supportedImageExt(ext string) bool {
	ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	switch ext {
	case "jpg", "jpeg", "png", "webp":
		return true
	}
	return false
}
