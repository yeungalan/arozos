package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Transport enumerates the RTSP transport strategies from the requirements
// (section 2.2).
const (
	TransportTCP  = "tcp"
	TransportUDP  = "udp"
	TransportAuto = "auto"
)

// Recording modes (section 4).
const (
	RecordingOff        = "off"
	RecordingContinuous = "continuous"
	RecordingMotion     = "motion"
	RecordingScheduled  = "scheduled"
	RecordingManual     = "manual"
)

// Camera status values (section 2.1 / 11).
const (
	StatusOnline   = "online"
	StatusOffline  = "offline"
	StatusDisabled = "disabled"
	StatusUnknown  = "unknown"
)

// validTransports / validCodecs / validRecordingModes are the accepted values
// for the respective fields. Codecs cover the required minimum set plus the
// "future" set noted in the spec so the UI can already offer them.
var (
	validTransports     = map[string]bool{TransportTCP: true, TransportUDP: true, TransportAuto: true}
	validCodecs         = map[string]bool{"h264": true, "h265": true, "mjpeg": true, "av1": true, "mpeg4": true, "": true}
	validRecordingModes = map[string]bool{
		RecordingOff: true, RecordingContinuous: true, RecordingMotion: true,
		RecordingScheduled: true, RecordingManual: true,
	}
	validRecordingFormats = map[string]bool{"mp4": true, "mkv": true, "": true}
)

// RecordingSettings captures the per-camera recording configuration
// (section 4). Enforcement of retention/storage is handled by the streaming
// engine; this foundation stores the intent.
type RecordingSettings struct {
	Mode          string `json:"mode"`          // one of the Recording* constants
	Format        string `json:"format"`        // mp4 | mkv
	RetentionDays int    `json:"retentionDays"` // 0 = keep until storage limit
	MaxStorageMB  int    `json:"maxStorageMb"`  // 0 = unlimited
}

// Camera is the persisted representation of a single IP camera. Credentials are
// stored so the backend can construct the final RTSP connection string on
// demand; the API never serialises the password back to clients (see redact).
type Camera struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	RTSPURL      string            `json:"rtspUrl"`
	Username     string            `json:"username"`
	Password     string            `json:"password,omitempty"`
	HasPassword  bool              `json:"hasPassword"`
	StreamType   string            `json:"streamType"` // e.g. "main" / "sub"
	Transport    string            `json:"transport"`
	Codec        string            `json:"codec"`
	Manufacturer string            `json:"manufacturer"`
	Model        string            `json:"model"`
	Resolution   string            `json:"resolution"`
	FPS          int               `json:"fps"`
	Recording    RecordingSettings `json:"recording"`
	GroupID      string            `json:"groupId"`
	Tags         []string          `json:"tags"`
	Enabled      bool              `json:"enabled"`
	Status       string            `json:"status"`
	LastSeen     int64             `json:"lastSeen"` // unix seconds, 0 = never
	CreatedAt    int64             `json:"createdAt"`
	UpdatedAt    int64             `json:"updatedAt"`
}

// normalise lower-cases and trims the enum-ish fields and applies defaults so
// callers may submit human-friendly values ("TCP", "H.264") and slightly
// sloppy input.
func (c *Camera) normalise() {
	c.Name = strings.TrimSpace(c.Name)
	c.RTSPURL = strings.TrimSpace(c.RTSPURL)
	c.Username = strings.TrimSpace(c.Username)
	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	if c.Transport == "" {
		c.Transport = TransportAuto
	}
	// Accept "H.264" / "H265" style input and fold to the canonical key.
	c.Codec = strings.ToLower(strings.TrimSpace(c.Codec))
	c.Codec = strings.ReplaceAll(c.Codec, ".", "")
	c.Codec = strings.ReplaceAll(c.Codec, "-", "")
	c.Recording.Mode = strings.ToLower(strings.TrimSpace(c.Recording.Mode))
	if c.Recording.Mode == "" {
		c.Recording.Mode = RecordingOff
	}
	c.Recording.Format = strings.ToLower(strings.TrimSpace(c.Recording.Format))

	cleanTags := make([]string, 0, len(c.Tags))
	seen := map[string]bool{}
	for _, t := range c.Tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		cleanTags = append(cleanTags, t)
	}
	c.Tags = cleanTags
}

// validate reports the first problem with the camera's user-supplied fields, or
// nil when the camera is well formed. It is intentionally strict about the RTSP
// URL because that is the field most likely to be wrong (spec section 2.2).
func (c *Camera) validate() error {
	if c.Name == "" {
		return errors.New("camera name is required")
	}
	if c.RTSPURL == "" {
		return errors.New("RTSP URL is required")
	}
	u, err := url.Parse(c.RTSPURL)
	if err != nil {
		return fmt.Errorf("invalid RTSP URL: %v", err)
	}
	if !strings.EqualFold(u.Scheme, "rtsp") && !strings.EqualFold(u.Scheme, "rtsps") {
		return errors.New("RTSP URL must start with rtsp:// or rtsps://")
	}
	if u.Host == "" {
		return errors.New("RTSP URL is missing a host")
	}
	if !validTransports[c.Transport] {
		return fmt.Errorf("unsupported transport %q (want tcp, udp or auto)", c.Transport)
	}
	if !validCodecs[c.Codec] {
		return fmt.Errorf("unsupported codec %q", c.Codec)
	}
	if c.FPS < 0 || c.FPS > 1000 {
		return errors.New("fps must be between 0 and 1000")
	}
	if !validRecordingModes[c.Recording.Mode] {
		return fmt.Errorf("unsupported recording mode %q", c.Recording.Mode)
	}
	if !validRecordingFormats[c.Recording.Format] {
		return fmt.Errorf("unsupported recording format %q (want mp4 or mkv)", c.Recording.Format)
	}
	if c.Recording.RetentionDays < 0 {
		return errors.New("retention days cannot be negative")
	}
	if c.Recording.MaxStorageMB < 0 {
		return errors.New("max storage cannot be negative")
	}
	return nil
}

// connectionURL returns the fully-formed RTSP URL with credentials embedded,
// building it from the separate Username/Password fields when the stored URL
// does not already carry userinfo. This is where the backend "securely
// constructs the final connection" (spec section 2.2) rather than trusting the
// client to interpolate credentials.
func (c *Camera) connectionURL() (string, error) {
	u, err := url.Parse(c.RTSPURL)
	if err != nil {
		return "", err
	}
	if u.User == nil && c.Username != "" {
		if c.Password != "" {
			u.User = url.UserPassword(c.Username, c.Password)
		} else {
			u.User = url.User(c.Username)
		}
	}
	return u.String(), nil
}

// redact returns a copy safe to serialise to API clients: the password is
// cleared and HasPassword reflects whether one is stored.
func (c Camera) redact() Camera {
	c.HasPassword = c.Password != ""
	c.Password = ""
	return c
}

// Group organises cameras by location / floor / building (spec section 2.1).
type Group struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // location | floor | building | ""
	ParentID  string `json:"parentId"`
	CreatedAt int64  `json:"createdAt"`
}

var validGroupKinds = map[string]bool{"location": true, "floor": true, "building": true, "": true}

// validate reports the first problem with a group definition.
func (g *Group) validate() error {
	g.Name = strings.TrimSpace(g.Name)
	g.Kind = strings.ToLower(strings.TrimSpace(g.Kind))
	if g.Name == "" {
		return errors.New("group name is required")
	}
	if !validGroupKinds[g.Kind] {
		return fmt.Errorf("unsupported group kind %q (want location, floor or building)", g.Kind)
	}
	return nil
}
