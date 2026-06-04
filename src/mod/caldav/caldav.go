package caldav

/*
	ArozOS CalDAV Server

	Provides CalDAV protocol support for bidirectional sync of Notes with
	iOS Reminders / Apple Calendar clients.

	Authentication: HTTP Basic Auth
	  - Username: ArozOS username
	  - Password: ArozOS autologin token

	URL structure:
	  /caldav/                                   root (requires auth)
	  /caldav/principals/{username}/             principal URL
	  /caldav/calendars/{username}/              calendar home set
	  /caldav/calendars/{username}/notes/        notes collection (VTODO)
	  /caldav/calendars/{username}/notes/{id}.ics individual note
*/

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	auth "imuslab.com/arozos/mod/auth"
	db "imuslab.com/arozos/mod/database"
)

// Server is the CalDAV server instance.
type Server struct {
	Enabled       bool
	rootDirectory string
	prefix        string
	authAgent     *auth.AuthAgent
	database      *db.Database
}

// NoteMeta mirrors the AGI notes metadata structure.
type NoteMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
}

// NotesMeta is the full metadata file structure (meta.json).
type NotesMeta struct {
	LastOpened string     `json:"lastOpened"`
	Theme      string     `json:"theme"`
	Notes      []NoteMeta `json:"notes"`
}

var safeNoteIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// NewServer creates and returns a CalDAV server.
func NewServer(prefix string, rootDirectory string, authAgent *auth.AuthAgent, database *db.Database) *Server {
	database.NewTable("caldav")
	enabled := true
	database.Read("caldav", "enabled", &enabled)
	return &Server{
		Enabled:       enabled,
		rootDirectory: filepath.Clean(rootDirectory),
		prefix:        strings.TrimSuffix(prefix, "/"),
		authAgent:     authAgent,
		database:      database,
	}
}

// SetEnabled persists the enabled/disabled state.
func (s *Server) SetEnabled(enabled bool) {
	s.Enabled = enabled
	s.database.Write("caldav", "enabled", enabled)
}

// ---- path helpers --------------------------------------------------------

func (s *Server) notesDir(username string) string {
	return filepath.Join(s.rootDirectory, "users", username, "Document", "Notes")
}

func (s *Server) metaFilePath(username string) string {
	return filepath.Join(s.notesDir(username), "meta.json")
}

func (s *Server) noteFilePath(username, noteID string) string {
	return filepath.Join(s.notesDir(username), noteID+".txt")
}

// ---- metadata helpers ----------------------------------------------------

func (s *Server) readMeta(username string) (*NotesMeta, error) {
	meta := &NotesMeta{Notes: []NoteMeta{}}
	raw, err := os.ReadFile(s.metaFilePath(username))
	if err != nil {
		if os.IsNotExist(err) {
			return meta, nil
		}
		return nil, err
	}
	if jsonErr := json.Unmarshal(raw, meta); jsonErr != nil {
		return meta, nil
	}
	if meta.Notes == nil {
		meta.Notes = []NoteMeta{}
	}
	return meta, nil
}

func (s *Server) writeMeta(username string, meta *NotesMeta) error {
	os.MkdirAll(s.notesDir(username), 0755)
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(s.metaFilePath(username), raw, 0644)
}

func (s *Server) upsertNoteMeta(meta *NotesMeta, noteID string, title string, updatedAt int64) {
	for i, n := range meta.Notes {
		if n.ID == noteID {
			meta.Notes[i].Title = title
			meta.Notes[i].UpdatedAt = updatedAt
			meta.LastOpened = noteID
			return
		}
	}
	meta.Notes = append(meta.Notes, NoteMeta{ID: noteID, Title: title, UpdatedAt: updatedAt})
	meta.LastOpened = noteID
}

// ---- iCalendar helpers ---------------------------------------------------

func escapeICS(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\r\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

func unescapeICS(s string) string {
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\N", "\n")
	s = strings.ReplaceAll(s, "\\,", ",")
	s = strings.ReplaceAll(s, "\\;", ";")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}

// foldICS folds lines at 75 chars per RFC 5545.
func foldICS(s string) string {
	var sb strings.Builder
	for _, line := range strings.Split(s, "\r\n") {
		if len(line) <= 75 {
			sb.WriteString(line + "\r\n")
			continue
		}
		sb.WriteString(line[:75] + "\r\n")
		rest := line[75:]
		for len(rest) > 74 {
			sb.WriteString(" " + rest[:74] + "\r\n")
			rest = rest[74:]
		}
		if rest != "" {
			sb.WriteString(" " + rest + "\r\n")
		}
	}
	return sb.String()
}

// noteToVTODO converts note content to a VCALENDAR/VTODO string.
func noteToVTODO(noteID, content string, updatedAt int64) string {
	title := "Note"
	desc := ""
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if t := strings.TrimSpace(line); t != "" {
			if len(t) > 60 {
				title = t[:60]
			} else {
				title = t
			}
			if i+1 < len(lines) {
				desc = strings.Join(lines[i+1:], "\n")
			}
			break
		}
	}

	if updatedAt <= 0 {
		updatedAt = time.Now().UnixMilli()
	}
	t := time.UnixMilli(updatedAt).UTC()
	stamp := t.Format("20060102T150405Z")
	uid := noteID + "@arozos-notes"

	raw := fmt.Sprintf("BEGIN:VCALENDAR\r\n"+
		"VERSION:2.0\r\n"+
		"PRODID:-//ArozOS//Notes CalDAV//EN\r\n"+
		"BEGIN:VTODO\r\n"+
		"UID:%s\r\n"+
		"SUMMARY:%s\r\n"+
		"DESCRIPTION:%s\r\n"+
		"DTSTAMP:%s\r\n"+
		"LAST-MODIFIED:%s\r\n"+
		"CREATED:%s\r\n"+
		"STATUS:NEEDS-ACTION\r\n"+
		"END:VTODO\r\n"+
		"END:VCALENDAR\r\n",
		uid, escapeICS(title), escapeICS(desc), stamp, stamp, stamp)

	return foldICS(raw)
}

// parseVTODO extracts summary, description, and UID from a VTODO.
// It handles both folded and unfolded iCalendar content.
func parseVTODO(ics string) (summary, description, uid string) {
	// Unfold RFC-5545 folded lines (CRLF + space/tab)
	ics = strings.ReplaceAll(ics, "\r\n ", "")
	ics = strings.ReplaceAll(ics, "\r\n\t", "")
	ics = strings.ReplaceAll(ics, "\r", "")

	inTodo := false
	for _, line := range strings.Split(ics, "\n") {
		switch {
		case line == "BEGIN:VTODO":
			inTodo = true
		case line == "END:VTODO":
			inTodo = false
		case inTodo && strings.HasPrefix(line, "SUMMARY:"):
			summary = unescapeICS(strings.TrimPrefix(line, "SUMMARY:"))
		case inTodo && strings.HasPrefix(line, "DESCRIPTION:"):
			description = unescapeICS(strings.TrimPrefix(line, "DESCRIPTION:"))
		case inTodo && strings.HasPrefix(line, "UID:"):
			uid = strings.TrimPrefix(line, "UID:")
		}
	}
	return
}

// noteContentFromVTODO rebuilds note content from VTODO fields.
func noteContentFromVTODO(summary, description string) string {
	if strings.TrimSpace(description) == "" {
		return summary
	}
	return summary + "\n" + description
}

// etag computes an MD5 ETag from note content.
func etag(content string) string {
	h := md5.New()
	h.Write([]byte(content))
	return `"` + hex.EncodeToString(h.Sum(nil)) + `"`
}

// collectionCTag computes a collection change tag from all note ETags.
func collectionCTag(notes []NoteMeta) string {
	h := md5.New()
	for _, n := range notes {
		h.Write([]byte(n.ID))
		h.Write([]byte(fmt.Sprint(n.UpdatedAt)))
	}
	return `"` + hex.EncodeToString(h.Sum(nil)) + `"`
}

// ---- authentication ------------------------------------------------------

// authenticate validates HTTP Basic Auth.
// Accepts either the user's ArozOS password or an autologin token as the password.
func (s *Server) authenticate(r *http.Request) (username string, ok bool) {
	u, password, hasBasic := r.BasicAuth()
	if !hasBasic || u == "" || password == "" {
		return "", false
	}

	// Accept autologin token as password
	if valid, owner := s.authAgent.ValidateAutoLoginToken(password); valid && owner == u {
		return u, true
	}

	// Accept regular ArozOS password
	if s.authAgent.ValidateUsernameAndPassword(u, password) {
		return u, true
	}

	return "", false
}

func sendUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="ArozOS CalDAV"`)
	w.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte("401 Unauthorized"))
}

// ---- main handler --------------------------------------------------------

// HandleRequest is the top-level HTTP handler for all CalDAV paths.
func (s *Server) HandleRequest(w http.ResponseWriter, r *http.Request) {
	if !s.Enabled {
		http.Error(w, "CalDAV service disabled", http.StatusServiceUnavailable)
		return
	}

	// Always set DAV header
	w.Header().Set("DAV", "1, 2, 3, calendar-access")

	if r.Method == http.MethodOptions {
		s.handleOptions(w, r)
		return
	}

	username, ok := s.authenticate(r)
	if !ok {
		sendUnauthorized(w)
		return
	}

	// Derive the path relative to the prefix
	path := r.URL.Path
	if s.prefix != "" {
		path = strings.TrimPrefix(path, s.prefix)
	}
	if path == "" {
		path = "/"
	}

	switch r.Method {
	case "PROPFIND":
		s.handlePropfind(w, r, path, username)
	case "REPORT":
		s.handleReport(w, r, path, username)
	case http.MethodGet, http.MethodHead:
		s.handleGet(w, r, path, username)
	case http.MethodPut:
		s.handlePut(w, r, path, username)
	case http.MethodDelete:
		s.handleDelete(w, r, path, username)
	default:
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, REPORT")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, REPORT")
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// ---- PROPFIND ------------------------------------------------------------

func (s *Server) handlePropfind(w http.ResponseWriter, r *http.Request, path, username string) {
	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "1"
	}

	prefix := s.prefix

	switch {
	// Root or well-known discovery
	case path == "/" || path == "":
		s.propfindRoot(w, username, prefix)

	// Principal
	case path == "/principals/"+username+"/" || path == "/principals/"+username:
		s.propfindPrincipal(w, username, prefix)

	// Calendar home
	case path == "/calendars/"+username+"/" || path == "/calendars/"+username:
		s.propfindCalendarHome(w, r, username, prefix, depth)

	// Notes collection
	case path == "/calendars/"+username+"/notes/" || path == "/calendars/"+username+"/notes":
		s.propfindNotesCollection(w, r, username, prefix, depth)

	// Individual note
	case strings.HasPrefix(path, "/calendars/"+username+"/notes/") && strings.HasSuffix(path, ".ics"):
		noteID := noteIDFromPath(path)
		s.propfindNoteItem(w, username, noteID, prefix)

	default:
		http.NotFound(w, r)
	}
}

func (s *Server) propfindRoot(w http.ResponseWriter, username, prefix string) {
	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(207)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:response>
    <D:href>%s/</D:href>
    <D:propstat>
      <D:prop>
        <D:current-user-principal><D:href>%s/principals/%s/</D:href></D:current-user-principal>
        <D:principal-URL><D:href>%s/principals/%s/</D:href></D:principal-URL>
        <D:resourcetype><D:collection/></D:resourcetype>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`, prefix, prefix, username, prefix, username)
}

func (s *Server) propfindPrincipal(w http.ResponseWriter, username, prefix string) {
	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(207)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/">
  <D:response>
    <D:href>%s/principals/%s/</D:href>
    <D:propstat>
      <D:prop>
        <D:displayname>%s</D:displayname>
        <D:principal-URL><D:href>%s/principals/%s/</D:href></D:principal-URL>
        <D:current-user-principal><D:href>%s/principals/%s/</D:href></D:current-user-principal>
        <C:calendar-home-set><D:href>%s/calendars/%s/</D:href></C:calendar-home-set>
        <C:calendar-user-address-set>
          <D:href>mailto:%s@arozos</D:href>
          <D:href>%s/principals/%s/</D:href>
        </C:calendar-user-address-set>
        <D:resourcetype><D:principal/><D:collection/></D:resourcetype>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`, prefix, username, username,
		prefix, username, prefix, username,
		prefix, username,
		username, prefix, username)
}

func (s *Server) propfindCalendarHome(w http.ResponseWriter, r *http.Request, username, prefix, depth string) {
	meta, _ := s.readMeta(username)
	ctag := collectionCTag(meta.Notes)

	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(207)
	homeResp := fmt.Sprintf(`  <D:response>
    <D:href>%s/calendars/%s/</D:href>
    <D:propstat>
      <D:prop>
        <D:resourcetype><D:collection/></D:resourcetype>
        <D:displayname>Calendar Home</D:displayname>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>`, prefix, username)

	notesResp := fmt.Sprintf(`  <D:response>
    <D:href>%s/calendars/%s/notes/</D:href>
    <D:propstat>
      <D:prop>
        <D:resourcetype><D:collection/><C:calendar/></D:resourcetype>
        <D:displayname>Notes</D:displayname>
        <CS:getctag>%s</CS:getctag>
        <C:supported-calendar-component-set>
          <C:comp name="VTODO"/>
        </C:supported-calendar-component-set>
        <D:current-user-privilege-set>
          <D:privilege><D:read/></D:privilege>
          <D:privilege><D:write/></D:privilege>
          <D:privilege><D:bind/></D:privilege>
          <D:privilege><D:unbind/></D:privilege>
        </D:current-user-privilege-set>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>`, prefix, username, ctag)

	inner := homeResp
	if depth != "0" {
		inner += "\n" + notesResp
	}

	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/">
%s
</D:multistatus>`, inner)
}

func (s *Server) propfindNotesCollection(w http.ResponseWriter, r *http.Request, username, prefix, depth string) {
	meta, _ := s.readMeta(username)
	ctag := collectionCTag(meta.Notes)

	var items strings.Builder
	if depth != "0" {
		// Sort notes by updatedAt descending for stability
		notes := make([]NoteMeta, len(meta.Notes))
		copy(notes, meta.Notes)
		sort.Slice(notes, func(i, j int) bool { return notes[i].UpdatedAt > notes[j].UpdatedAt })

		for _, n := range notes {
			content, err := os.ReadFile(s.noteFilePath(username, n.ID))
			if err != nil {
				continue
			}
			tag := etag(string(content))
			items.WriteString(fmt.Sprintf(`  <D:response>
    <D:href>%s/calendars/%s/notes/%s.ics</D:href>
    <D:propstat>
      <D:prop>
        <D:getetag>%s</D:getetag>
        <D:resourcetype/>
        <D:getcontenttype>text/calendar; charset=utf-8; component=VTODO</D:getcontenttype>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
`, prefix, username, n.ID, tag))
		}
	}

	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(207)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/">
  <D:response>
    <D:href>%s/calendars/%s/notes/</D:href>
    <D:propstat>
      <D:prop>
        <D:resourcetype><D:collection/><C:calendar/></D:resourcetype>
        <D:displayname>Notes</D:displayname>
        <CS:getctag>%s</CS:getctag>
        <D:sync-token>%s/calendars/%s/notes/?token=%s</D:sync-token>
        <C:supported-calendar-component-set>
          <C:comp name="VTODO"/>
        </C:supported-calendar-component-set>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
%s</D:multistatus>`, prefix, username, ctag, prefix, username, strings.Trim(ctag, `"`), items.String())
}

func (s *Server) propfindNoteItem(w http.ResponseWriter, username, noteID, prefix string) {
	if !safeNoteIDRe.MatchString(noteID) {
		http.Error(w, "Bad note ID", http.StatusBadRequest)
		return
	}
	content, err := os.ReadFile(s.noteFilePath(username, noteID))
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	tag := etag(string(content))
	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(207)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:response>
    <D:href>%s/calendars/%s/notes/%s.ics</D:href>
    <D:propstat>
      <D:prop>
        <D:getetag>%s</D:getetag>
        <D:resourcetype/>
        <D:getcontenttype>text/calendar; charset=utf-8; component=VTODO</D:getcontenttype>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`, prefix, username, noteID, tag)
}

// ---- REPORT --------------------------------------------------------------

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request, path, username string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	bodyStr := string(body)

	prefix := s.prefix

	// Determine if this is a multiget or query
	isMultiget := strings.Contains(bodyStr, "calendar-multiget")
	// isQuery    := strings.Contains(bodyStr, "calendar-query")

	meta, _ := s.readMeta(username)

	var wantHrefs []string
	if isMultiget {
		// Extract hrefs from the request body
		wantHrefs = extractHrefs(bodyStr)
	}

	wantCalendarData := strings.Contains(bodyStr, "calendar-data")
	wantEtag := strings.Contains(bodyStr, "getetag")

	var responses strings.Builder
	for _, n := range meta.Notes {
		// If multiget with specific hrefs, filter
		if isMultiget && len(wantHrefs) > 0 {
			found := false
			for _, h := range wantHrefs {
				if strings.Contains(h, n.ID+".ics") {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		content, err := os.ReadFile(s.noteFilePath(username, n.ID))
		if err != nil {
			continue
		}
		tag := etag(string(content))
		vtodo := noteToVTODO(n.ID, string(content), n.UpdatedAt)

		var propEntries strings.Builder
		if wantEtag {
			propEntries.WriteString(fmt.Sprintf("        <D:getetag>%s</D:getetag>\n", tag))
		}
		if wantCalendarData {
			propEntries.WriteString("        <C:calendar-data><![CDATA[" + vtodo + "]]></C:calendar-data>\n")
		}

		responses.WriteString(fmt.Sprintf(`  <D:response>
    <D:href>%s/calendars/%s/notes/%s.ics</D:href>
    <D:propstat>
      <D:prop>
%s      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
`, prefix, username, n.ID, propEntries.String()))
	}

	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(207)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
%s</D:multistatus>`, responses.String())
}

// extractHrefs extracts D:href values from an XML body string.
func extractHrefs(body string) []string {
	var hrefs []string
	parts := strings.Split(body, "<")
	for _, p := range parts {
		if strings.HasPrefix(p, "D:href>") || strings.HasPrefix(p, "href>") {
			end := strings.Index(p, "</")
			if end == -1 {
				end = strings.Index(p, "<")
			}
			raw := p
			if idx := strings.Index(raw, ">"); idx >= 0 {
				raw = raw[idx+1:]
			}
			if idx := strings.Index(raw, "<"); idx >= 0 {
				raw = raw[:idx]
			}
			if raw != "" {
				hrefs = append(hrefs, raw)
			}
		}
	}
	return hrefs
}

// ---- GET -----------------------------------------------------------------

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request, path, username string) {
	if !strings.HasSuffix(path, ".ics") {
		http.NotFound(w, r)
		return
	}
	noteID := noteIDFromPath(path)
	if !safeNoteIDRe.MatchString(noteID) {
		http.Error(w, "Bad note ID", http.StatusBadRequest)
		return
	}

	meta, _ := s.readMeta(username)
	updatedAt := int64(0)
	for _, n := range meta.Notes {
		if n.ID == noteID {
			updatedAt = n.UpdatedAt
			break
		}
	}

	content, err := os.ReadFile(s.noteFilePath(username, noteID))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	vtodo := noteToVTODO(noteID, string(content), updatedAt)
	tag := etag(string(content))

	w.Header().Set("Content-Type", "text/calendar; charset=UTF-8")
	w.Header().Set("ETag", tag)
	if r.Method == http.MethodHead {
		return
	}
	w.Write([]byte(vtodo))
}

// ---- PUT -----------------------------------------------------------------

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request, path, username string) {
	if !strings.HasSuffix(path, ".ics") {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	noteID := noteIDFromPath(path)
	if !safeNoteIDRe.MatchString(noteID) {
		http.Error(w, "Bad note ID", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad request body", http.StatusBadRequest)
		return
	}

	summary, description, _ := parseVTODO(string(body))
	noteContent := noteContentFromVTODO(summary, description)

	if err := os.MkdirAll(s.notesDir(username), 0755); err != nil {
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(s.noteFilePath(username, noteID), []byte(noteContent), 0644); err != nil {
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	now := time.Now().UnixMilli()
	title := summary
	if len(title) > 60 {
		title = title[:60]
	}

	meta, _ := s.readMeta(username)
	s.upsertNoteMeta(meta, noteID, title, now)
	s.writeMeta(username, meta)

	tag := etag(noteContent)
	w.Header().Set("ETag", tag)
	w.WriteHeader(http.StatusCreated)
}

// ---- DELETE --------------------------------------------------------------

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, path, username string) {
	if !strings.HasSuffix(path, ".ics") {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	noteID := noteIDFromPath(path)
	if !safeNoteIDRe.MatchString(noteID) {
		http.Error(w, "Bad note ID", http.StatusBadRequest)
		return
	}

	notePath := s.noteFilePath(username, noteID)
	if err := os.Remove(notePath); err != nil && !os.IsNotExist(err) {
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	meta, _ := s.readMeta(username)
	newNotes := meta.Notes[:0]
	for _, n := range meta.Notes {
		if n.ID != noteID {
			newNotes = append(newNotes, n)
		}
	}
	meta.Notes = newNotes
	if meta.LastOpened == noteID {
		if len(meta.Notes) > 0 {
			meta.LastOpened = meta.Notes[0].ID
		} else {
			meta.LastOpened = ""
		}
	}
	s.writeMeta(username, meta)

	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers -------------------------------------------------------------

func noteIDFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".ics")
}
