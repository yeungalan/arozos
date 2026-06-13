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
	"strings"
	"sync"

	"imuslab.com/arozos/mod/database"
	user "imuslab.com/arozos/mod/user"
)

const (
	configTable = "facerecog"        //Table for the system wide configuration
	facesTable  = "facerecog_faces"  //Table for per-photo face entries, key: username/sha256(vpath)
	peopleTable = "facerecog_people" //Table for per-user people clusters, key: username/personID

	configKey            = "config"
	descriptorVersionKey = "descriptorVersion" //Stores the descriptorVersion of the data on disk

	//Hard limits to keep a single request bounded
	maxPathsPerScan  = 32       //Maximum number of photos per scan request
	maxFacesPerPhoto = 16       //Maximum number of faces stored for one photo
	maxImageFileSize = 25 << 20 //25MB, same limit as the system thumbnail renderer
)

// Config is the system-wide face recognition configuration, managed by
// administrators from the AI Integration tab in System Settings.
type Config struct {
	Enabled        bool    `json:"enabled"`        //Master switch for the whole feature
	MinFaceSize    int     `json:"minFaceSize"`    //Minimum face size in px (in the detection-scaled image)
	MatchThreshold float64 `json:"matchThreshold"` //Maximum descriptor distance for two faces to be the same person
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
}

// DefaultConfig returns the configuration used when nothing has been saved yet.
// The feature ships disabled so it stays strictly opt-in.
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		MinFaceSize:    60,
		MatchThreshold: 0.42,
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

	//Clear stored face data left over from an older, incompatible descriptor
	//format so it is re-scanned with the current one.
	manager.migrateDescriptorVersion()

	return manager, nil
}

// migrateDescriptorVersion wipes stored faces and people when the descriptor
// format on disk predates the current descriptorVersion. The Photo app then
// re-scans automatically on next use, regrouping people with the new
// descriptor. Configuration (the on/off switch and tuning) is preserved.
func (m *Manager) migrateDescriptorVersion() {
	stored := 0
	if m.options.Database.KeyExists(configTable, descriptorVersionKey) {
		m.options.Database.Read(configTable, descriptorVersionKey, &stored)
	}
	if stored == descriptorVersion {
		return
	}
	m.ClearAllData()
	m.options.Database.Write(configTable, descriptorVersionKey, descriptorVersion)
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
