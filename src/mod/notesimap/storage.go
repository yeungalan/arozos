package notesimap

/*
	storage.go - maps IMAP mailbox state onto the same files the Notes web-app
	uses (user:/Document/Notes/meta.json and user:/Document/Notes/{id}.txt),
	so the desktop web-app and Apple Notes read and write the same data with
	no migration step.
*/

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	imap "github.com/emersion/go-imap"
	"imuslab.com/arozos/mod/user"
)

// noteEntry mirrors one element of meta.json's "notes" array. Uid and Flags
// are additive fields the Notes web-app's AGI scripts never write, but also
// never strip when they rewrite an existing entry (they only touch Title and
// UpdatedAt), so both sides of the sync can share the same file untouched.
type noteEntry struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	UpdatedAt int64    `json:"updatedAt"`
	Uid       uint32   `json:"uid,omitempty"`
	Flags     []string `json:"flags,omitempty"`
}

// noteMeta mirrors the Notes web-app's meta.json.
type noteMeta struct {
	LastOpened  string      `json:"lastOpened"`
	Theme       string      `json:"theme"`
	UidNext     uint32      `json:"uidNext,omitempty"`
	UidValidity uint32      `json:"uidValidity,omitempty"`
	Notes       []noteEntry `json:"notes"`
}

// idPattern matches the same safe-id charset enforced by the Notes web-app's
// backend/*.agi scripts.
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// pathResolver turns a user's virtual path (e.g. "/Document/Notes/meta.json")
// into a real filesystem path. Factored out of store so tests can point it at
// a temp directory instead of standing up a full user.UserHandler.
type pathResolver func(username, vpath string) (string, error)

// store resolves and persists the Notes files for one user. A single mutex
// guards every user's meta.json, mirroring the CalDAV handler's approach of
// one lock for all read-modify-write cycles (traffic is low; contention is
// not a concern).
type store struct {
	resolve pathResolver
	mu      sync.Mutex
}

func newStore(uh *user.UserHandler) *store {
	return &store{resolve: func(username, vpath string) (string, error) {
		userObj, err := uh.GetUserInfoFromUsername(username)
		if err != nil {
			return "", err
		}
		fsh, err := userObj.GetHomeFileSystemHandler()
		if err != nil {
			return "", err
		}
		return fsh.FileSystemAbstraction.VirtualPathToRealPath(vpath, username)
	}}
}

func newStoreWithResolver(resolve pathResolver) *store {
	return &store{resolve: resolve}
}

func (s *store) realPath(username, vpath string) (string, error) {
	return s.resolve(username, vpath)
}

func (s *store) metaPath(username string) (string, error) {
	return s.realPath(username, "/Document/Notes/meta.json")
}

func (s *store) notePath(username, id string) (string, error) {
	return s.realPath(username, "/Document/Notes/"+id+".txt")
}

// loadMeta reads meta.json, assigning a stable IMAP UID and default flags to
// any note the web-app created or edited without them, and persisting those
// additions immediately so UIDs never change across restarts.
func (s *store) loadMeta(username string) (*noteMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadMetaLocked(username)
}

// withMeta loads meta.json, applies fn, and - if fn reports a change -
// persists the result, all while holding the store's lock so the whole
// read-modify-write cycle is atomic instead of just its load/save halves.
func (s *store) withMeta(username string, fn func(*noteMeta) (bool, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta, err := s.loadMetaLocked(username)
	if err != nil {
		return err
	}
	changed, err := fn(meta)
	if err != nil {
		return err
	}
	if changed {
		return s.saveMetaLocked(username, meta)
	}
	return nil
}

func (s *store) loadMetaLocked(username string) (*noteMeta, error) {
	p, err := s.metaPath(username)
	if err != nil {
		return nil, err
	}

	meta := &noteMeta{Theme: "dark", Notes: []noteEntry{}}
	data, err := os.ReadFile(p)
	if err == nil {
		json.Unmarshal(data, meta) //nolint - fall back to defaults on malformed JSON
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if meta.Notes == nil {
		meta.Notes = []noteEntry{}
	}

	if backfillUids(meta) {
		if err := s.saveMetaLocked(username, meta); err != nil {
			return nil, err
		}
	}
	return meta, nil
}

// backfillUids assigns UidValidity/UidNext/per-note Uid and default flags
// where missing, then sorts notes by Uid so message sequence numbers stay in
// ascending UID order as RFC 3501 requires. Returns true if meta was changed.
func backfillUids(meta *noteMeta) bool {
	changed := false
	if meta.UidValidity == 0 {
		meta.UidValidity = uint32(time.Now().Unix())
		changed = true
	}
	if meta.UidNext == 0 {
		meta.UidNext = 1
		changed = true
	}
	for i := range meta.Notes {
		if meta.Notes[i].Uid == 0 {
			meta.Notes[i].Uid = meta.UidNext
			meta.UidNext++
			changed = true
		}
		if len(meta.Notes[i].Flags) == 0 {
			meta.Notes[i].Flags = []string{imap.SeenFlag}
			changed = true
		}
	}
	sort.Slice(meta.Notes, func(i, j int) bool { return meta.Notes[i].Uid < meta.Notes[j].Uid })
	return changed
}

func (s *store) saveMeta(username string, meta *noteMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveMetaLocked(username, meta)
}

func (s *store) saveMetaLocked(username string, meta *noteMeta) error {
	p, err := s.metaPath(username)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	if meta.Notes == nil {
		meta.Notes = []noteEntry{}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0644)
}

func (s *store) readNoteContent(username, id string) (string, error) {
	p, err := s.notePath(username, id)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func (s *store) writeNoteContent(username, id, content string) error {
	p, err := s.notePath(username, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0644)
}

func (s *store) deleteNoteContent(username, id string) error {
	p, err := s.notePath(username, id)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// newNoteID mints a note id compatible with the safe-id charset the web-app
// AGI scripts enforce, and guaranteed unused in meta.
func newNoteID(meta *noteMeta) string {
	existing := make(map[string]bool, len(meta.Notes))
	for _, n := range meta.Notes {
		existing[n.ID] = true
	}
	for {
		candidate := "note" + strconv.FormatInt(time.Now().UnixNano(), 36)
		if !existing[candidate] {
			return candidate
		}
		time.Sleep(time.Nanosecond)
	}
}

// deriveTitle returns the first non-empty line of content, truncated to 60
// characters, matching the Notes web-app's write.agi title derivation.
func deriveTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if len(trimmed) > 60 {
				return trimmed[:60]
			}
			return trimmed
		}
	}
	return "New Note"
}
