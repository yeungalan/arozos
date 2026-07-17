package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when a camera or group id does not exist.
var ErrNotFound = errors.New("not found")

// persistedState is the on-disk shape of the store.
type persistedState struct {
	Cameras []*Camera `json:"cameras"`
	Groups  []*Group  `json:"groups"`
}

// Store is an in-memory camera/group registry backed by an atomically-written
// JSON file. It is safe for concurrent use. A JSON file (rather than an
// embedded SQL engine) keeps the subservice a single, cgo-free, cross-platform
// binary — matching the ArozOS portability rule — while still being durable.
type Store struct {
	mu      sync.RWMutex
	path    string
	cameras map[string]*Camera
	groups  map[string]*Group
}

// NewStore loads the store from path, creating an empty one (and its parent
// directory) when the file does not yet exist.
func NewStore(path string) (*Store, error) {
	s := &Store{
		path:    path,
		cameras: map[string]*Camera{},
		groups:  map[string]*Group{},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var state persistedState
	if len(data) > 0 {
		if err := json.Unmarshal(data, &state); err != nil {
			return nil, err
		}
	}
	for _, c := range state.Cameras {
		s.cameras[c.ID] = c
	}
	for _, g := range state.Groups {
		s.groups[g.ID] = g
	}
	return s, nil
}

// newID returns a short random hex identifier.
func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// rand.Read essentially never fails; fall back to a time-based id.
		return "id" + time.Now().Format("20060102150405.000000")
	}
	return hex.EncodeToString(b)
}

// save writes the current state to disk atomically (temp file + rename). The
// caller must hold at least a read lock; callers that mutate hold the write
// lock, which also excludes concurrent saves.
func (s *Store) save() error {
	state := persistedState{
		Cameras: make([]*Camera, 0, len(s.cameras)),
		Groups:  make([]*Group, 0, len(s.groups)),
	}
	for _, c := range s.cameras {
		state.Cameras = append(state.Cameras, c)
	}
	for _, g := range s.groups {
		state.Groups = append(state.Groups, g)
	}
	sort.Slice(state.Cameras, func(i, j int) bool { return state.Cameras[i].CreatedAt < state.Cameras[j].CreatedAt })
	sort.Slice(state.Groups, func(i, j int) bool { return state.Groups[i].CreatedAt < state.Groups[j].CreatedAt })

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ---- Camera operations ---------------------------------------------------

// ListCameras returns all cameras, redacted, ordered by creation time.
func (s *Store) ListCameras() []Camera {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Camera, 0, len(s.cameras))
	for _, c := range s.cameras {
		out = append(out, c.redact())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// GetCamera returns a single redacted camera by id.
func (s *Store) GetCamera(id string) (Camera, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cameras[id]
	if !ok {
		return Camera{}, ErrNotFound
	}
	return c.redact(), nil
}

// getRaw returns the stored pointer (with credentials) for internal use such as
// building a connection URL. Caller must hold a lock.
func (s *Store) getRaw(id string) (*Camera, bool) {
	c, ok := s.cameras[id]
	return c, ok
}

// AddCamera validates and stores a new camera, returning the redacted result.
func (s *Store) AddCamera(c Camera) (Camera, error) {
	c.normalise()
	if err := c.validate(); err != nil {
		return Camera{}, err
	}
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	c.ID = newID()
	c.CreatedAt = now
	c.UpdatedAt = now
	if c.GroupID != "" {
		if _, ok := s.groups[c.GroupID]; !ok {
			return Camera{}, errors.New("group does not exist")
		}
	}
	if c.Enabled {
		c.Status = StatusUnknown
	} else {
		c.Status = StatusDisabled
	}
	stored := c
	s.cameras[c.ID] = &stored
	if err := s.save(); err != nil {
		delete(s.cameras, c.ID)
		return Camera{}, err
	}
	return stored.redact(), nil
}

// UpdateCamera applies the mutable fields of in to the stored camera. A blank
// password preserves the existing one so clients need not resend secrets.
func (s *Store) UpdateCamera(id string, in Camera) (Camera, error) {
	in.normalise()
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.cameras[id]
	if !ok {
		return Camera{}, ErrNotFound
	}
	if in.GroupID != "" {
		if _, ok := s.groups[in.GroupID]; !ok {
			return Camera{}, errors.New("group does not exist")
		}
	}
	// Preserve the password when the client sends an empty one.
	if in.Password == "" {
		in.Password = existing.Password
	}
	in.ID = existing.ID
	in.CreatedAt = existing.CreatedAt
	in.UpdatedAt = time.Now().Unix()
	in.LastSeen = existing.LastSeen
	if err := in.validate(); err != nil {
		return Camera{}, err
	}
	if !in.Enabled {
		in.Status = StatusDisabled
	} else if existing.Status == StatusDisabled || existing.Status == "" {
		in.Status = StatusUnknown
	} else {
		in.Status = existing.Status
	}
	prev := *existing
	*existing = in
	if err := s.save(); err != nil {
		*existing = prev
		return Camera{}, err
	}
	return existing.redact(), nil
}

// DeleteCamera removes a camera by id.
func (s *Store) DeleteCamera(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cameras[id]
	if !ok {
		return ErrNotFound
	}
	delete(s.cameras, id)
	if err := s.save(); err != nil {
		s.cameras[id] = c
		return err
	}
	return nil
}

// SetEnabled toggles a camera on or off, updating its status accordingly.
func (s *Store) SetEnabled(id string, enabled bool) (Camera, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cameras[id]
	if !ok {
		return Camera{}, ErrNotFound
	}
	prev := *c
	c.Enabled = enabled
	c.UpdatedAt = time.Now().Unix()
	if enabled {
		c.Status = StatusUnknown
	} else {
		c.Status = StatusDisabled
	}
	if err := s.save(); err != nil {
		*c = prev
		return Camera{}, err
	}
	return c.redact(), nil
}

// SetStatus records a health probe result (online/offline) and, when online,
// stamps LastSeen. Used by the status/validation endpoints and, in future, the
// health monitor.
func (s *Store) SetStatus(id, status string, seen bool) (Camera, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cameras[id]
	if !ok {
		return Camera{}, ErrNotFound
	}
	prev := *c
	c.Status = status
	if seen {
		c.LastSeen = time.Now().Unix()
	}
	if err := s.save(); err != nil {
		*c = prev
		return Camera{}, err
	}
	return c.redact(), nil
}

// SearchCameras filters cameras by the supplied criteria (spec section 14). All
// filters are ANDed; empty filters match everything. Text query matches name,
// description and manufacturer case-insensitively.
type SearchQuery struct {
	Text         string
	Status       string
	GroupID      string
	Tag          string
	Manufacturer string
}

// SearchCameras returns the redacted cameras matching q.
func (s *Store) SearchCameras(q SearchQuery) []Camera {
	s.mu.RLock()
	defer s.mu.RUnlock()
	text := strings.ToLower(strings.TrimSpace(q.Text))
	tag := strings.ToLower(strings.TrimSpace(q.Tag))
	manu := strings.ToLower(strings.TrimSpace(q.Manufacturer))
	out := make([]Camera, 0)
	for _, c := range s.cameras {
		if q.Status != "" && c.Status != q.Status {
			continue
		}
		if q.GroupID != "" && c.GroupID != q.GroupID {
			continue
		}
		if manu != "" && !strings.Contains(strings.ToLower(c.Manufacturer), manu) {
			continue
		}
		if tag != "" && !hasTag(c.Tags, tag) {
			continue
		}
		if text != "" {
			hay := strings.ToLower(c.Name + " " + c.Description + " " + c.Manufacturer)
			if !strings.Contains(hay, text) {
				continue
			}
		}
		out = append(out, c.redact())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if strings.ToLower(t) == want {
			return true
		}
	}
	return false
}

// Tags returns the distinct set of tags in use across all cameras, sorted.
func (s *Store) Tags() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]string{} // lower -> original
	for _, c := range s.cameras {
		for _, t := range c.Tags {
			seen[strings.ToLower(t)] = t
		}
	}
	out := make([]string, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// ---- Group operations ----------------------------------------------------

// ListGroups returns all groups ordered by creation time.
func (s *Store) ListGroups() []Group {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Group, 0, len(s.groups))
	for _, g := range s.groups {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// AddGroup validates and stores a new group.
func (s *Store) AddGroup(g Group) (Group, error) {
	if err := g.validate(); err != nil {
		return Group{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if g.ParentID != "" {
		if _, ok := s.groups[g.ParentID]; !ok {
			return Group{}, errors.New("parent group does not exist")
		}
	}
	g.ID = newID()
	g.CreatedAt = time.Now().Unix()
	stored := g
	s.groups[g.ID] = &stored
	if err := s.save(); err != nil {
		delete(s.groups, g.ID)
		return Group{}, err
	}
	return stored, nil
}

// UpdateGroup renames / re-kinds an existing group.
func (s *Store) UpdateGroup(id string, in Group) (Group, error) {
	if err := in.validate(); err != nil {
		return Group{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		return Group{}, ErrNotFound
	}
	prev := *g
	g.Name = in.Name
	g.Kind = in.Kind
	if err := s.save(); err != nil {
		*g = prev
		return Group{}, err
	}
	return *g, nil
}

// DeleteGroup removes a group and clears the GroupID of any camera that
// referenced it, so cameras are never orphaned into a dangling group.
func (s *Store) DeleteGroup(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		return ErrNotFound
	}
	delete(s.groups, id)
	cleared := map[string]int64{}
	for _, c := range s.cameras {
		if c.GroupID == id {
			cleared[c.ID] = c.UpdatedAt
			c.GroupID = ""
			c.UpdatedAt = time.Now().Unix()
		}
	}
	if err := s.save(); err != nil {
		// Roll back both the group deletion and the camera edits.
		s.groups[id] = g
		for cid, ts := range cleared {
			if c, ok := s.cameras[cid]; ok {
				c.GroupID = id
				c.UpdatedAt = ts
			}
		}
		return err
	}
	return nil
}
