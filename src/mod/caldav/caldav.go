package caldav

/*
	CalDAV Server Module for ArozOS
	Provides RFC 4791 CalDAV access to Notes app data as VTODO items.

	Authentication: HTTP Basic Auth
	  - Username: arozos username
	  - Password: auto-login token (obtain from My Account → Security)

	URL structure (prefix: /caldav):
	  /caldav/                             → discovery (current-user-principal)
	  /caldav/principals/{user}/           → principal resource
	  /caldav/{user}/                      → calendar home set
	  /caldav/{user}/notes/               → notes calendar (VTODO)
	  /caldav/{user}/notes/{id}.ics       → individual note

	Notes are stored at user:/Document/Notes/{id}.txt with meta.json.
	CalDAV exposes each note as a VTODO item; SUMMARY = title, DESCRIPTION = body.

	The server does not use any non-stdlib / non-commercial dependencies beyond
	what is already present in the arozos module graph.
*/

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	db "imuslab.com/arozos/mod/database"
	"imuslab.com/arozos/mod/user"
)

const prodID = "-//ArozOS//CalDAV Notes//EN"

// Manager manages the CalDAV endpoint.
type Manager struct {
	UserHandler *user.UserHandler
	Database    *db.Database
	Enabled     bool
}

// noteMeta mirrors the per-note entry in meta.json.
type noteMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"` // milliseconds since epoch
}

// notesMeta mirrors the full meta.json file.
type notesMeta struct {
	LastOpened string     `json:"lastOpened"`
	Theme      string     `json:"theme"`
	Notes      []noteMeta `json:"notes"`
}

// NewManager creates a Manager and reads the persisted enabled state.
func NewManager(userHandler *user.UserHandler, database *db.Database) *Manager {
	enabled := false
	database.Read("caldav", "enabled", &enabled)
	return &Manager{
		UserHandler: userHandler,
		Database:    database,
		Enabled:     enabled,
	}
}

// HandleToggle is an admin-only endpoint to enable/disable CalDAV.
func (m *Manager) HandleToggle(w http.ResponseWriter, r *http.Request) {
	state := r.FormValue("enable")
	m.Enabled = state == "true"
	m.Database.Write("caldav", "enabled", m.Enabled)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"enabled":%v}`, m.Enabled)
}

// HandleStatus returns the current enabled state as JSON.
func (m *Manager) HandleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"enabled":%v}`, m.Enabled)
}

// HandleRequest is the main CalDAV HTTP handler (mounted at /caldav/).
func (m *Manager) HandleRequest(w http.ResponseWriter, r *http.Request) {
	if !m.Enabled {
		http.Error(w, "CalDAV service is disabled", http.StatusServiceUnavailable)
		return
	}

	// Advertise DAV capabilities on every response.
	w.Header().Set("DAV", "1, 2, calendar-access")

	if r.Method == http.MethodOptions {
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, REPORT, MKCALENDAR")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Basic Auth: username + auto-login token.
	username, token, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="ArozOS CalDAV"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	authAgent := m.UserHandler.GetAuthAgent()
	valid, tokenOwner := authAgent.ValidateAutoLoginToken(token)
	if !valid || tokenOwner != username {
		w.Header().Set("WWW-Authenticate", `Basic realm="ArozOS CalDAV"`)
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	userinfo, err := m.UserHandler.GetUserInfoFromUsername(username)
	if err != nil {
		http.Error(w, "User not found", http.StatusUnauthorized)
		return
	}

	// Strip /caldav prefix; normalise to always start with "/".
	path := strings.TrimPrefix(r.URL.Path, "/caldav")
	if path == "" {
		path = "/"
	}

	m.route(w, r, path, username, userinfo)
}

// ─────────────────────────────────────────────────────────────────────────────
// Routing
// ─────────────────────────────────────────────────────────────────────────────

func (m *Manager) route(w http.ResponseWriter, r *http.Request, path, username string, userinfo *user.User) {
	principalPath := "/principals/" + username + "/"
	homePath := "/" + username + "/"
	calPath := "/" + username + "/notes/"

	// Normalise trailing slash for matching.
	normPath := path
	if normPath != "/" && !strings.HasSuffix(normPath, "/") {
		normPath += "/"
	}

	switch {
	case normPath == "/":
		m.handleDiscovery(w, r, username)

	case normPath == principalPath:
		m.handlePrincipal(w, r, username)

	case normPath == homePath:
		m.handleCalendarHome(w, r, username)

	case normPath == calPath:
		m.handleCalendar(w, r, username, userinfo)

	case strings.HasPrefix(normPath, calPath) && strings.HasSuffix(path, ".ics"):
		noteID := strings.TrimSuffix(strings.TrimPrefix(path, calPath), ".ics")
		if !validID(noteID) {
			http.Error(w, "Invalid note ID", http.StatusBadRequest)
			return
		}
		m.handleItem(w, r, username, noteID, userinfo)

	default:
		http.NotFound(w, r)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CalDAV resource handlers
// ─────────────────────────────────────────────────────────────────────────────

// handleDiscovery responds to PROPFIND / with current-user-principal.
func (m *Manager) handleDiscovery(w http.ResponseWriter, r *http.Request, username string) {
	if r.Method != "PROPFIND" {
		w.Header().Set("Allow", "OPTIONS, PROPFIND")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	principalHref := "/caldav/principals/" + username + "/"
	body := xmlMultistatus(
		xmlResponse("/caldav/",
			xmlPropstat(http.StatusOK,
				`<current-user-principal><href>`+xmlEsc(principalHref)+`</href></current-user-principal>`,
				`<resourcetype><collection/></resourcetype>`,
				`<displayname>ArozOS CalDAV</displayname>`,
			),
		),
	)
	writeXML(w, http.StatusMultiStatus, body)
}

// handlePrincipal responds to PROPFIND /principals/{user}/.
func (m *Manager) handlePrincipal(w http.ResponseWriter, r *http.Request, username string) {
	if r.Method != "PROPFIND" {
		w.Header().Set("Allow", "OPTIONS, PROPFIND")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	href := "/caldav/principals/" + username + "/"
	home := "/caldav/" + username + "/"
	body := xmlMultistatus(
		xmlResponse(href,
			xmlPropstat(http.StatusOK,
				`<displayname>`+xmlEsc(username)+`</displayname>`,
				`<principal-URL><href>`+xmlEsc(href)+`</href></principal-URL>`,
				`<resourcetype><collection/><principal/></resourcetype>`,
				`<C:calendar-home-set xmlns:C="urn:ietf:params:xml:ns:caldav"><href>`+xmlEsc(home)+`</href></C:calendar-home-set>`,
				`<C:calendar-user-address-set xmlns:C="urn:ietf:params:xml:ns:caldav"><href>mailto:`+xmlEsc(username)+`@arozos.local</href></C:calendar-user-address-set>`,
			),
		),
	)
	writeXML(w, http.StatusMultiStatus, body)
}

// handleCalendarHome responds to PROPFIND /{user}/ with the notes calendar listed.
func (m *Manager) handleCalendarHome(w http.ResponseWriter, r *http.Request, username string) {
	if r.Method != "PROPFIND" {
		w.Header().Set("Allow", "OPTIONS, PROPFIND")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	homeHref := "/caldav/" + username + "/"
	calHref := "/caldav/" + username + "/notes/"

	homeResp := xmlResponse(homeHref,
		xmlPropstat(http.StatusOK,
			`<resourcetype><collection/></resourcetype>`,
			`<displayname>`+xmlEsc(username)+`</displayname>`,
		),
	)

	calResp := ""
	depth := r.Header.Get("Depth")
	if depth == "1" || depth == "infinity" {
		calResp = "\n" + xmlResponse(calHref,
			xmlPropstat(http.StatusOK,
				`<resourcetype><collection/><C:calendar xmlns:C="urn:ietf:params:xml:ns:caldav"/></resourcetype>`,
				`<displayname>Notes</displayname>`,
				`<CS:getctag xmlns:CS="http://calendarserver.org/ns/">`+calCTag()+`</CS:getctag>`,
				`<C:supported-calendar-component-set xmlns:C="urn:ietf:params:xml:ns:caldav"><C:comp name="VTODO"/></C:supported-calendar-component-set>`,
				`<C:calendar-description xmlns:C="urn:ietf:params:xml:ns:caldav">ArozOS Notes</C:calendar-description>`,
			),
		)
	}

	writeXML(w, http.StatusMultiStatus, xmlMultistatus(homeResp+calResp))
}

// handleCalendar handles PROPFIND, REPORT, and MKCALENDAR on the notes calendar.
func (m *Manager) handleCalendar(w http.ResponseWriter, r *http.Request, username string, userinfo *user.User) {
	switch r.Method {
	case "PROPFIND":
		m.calendarPropfind(w, r, username, userinfo)
	case "REPORT":
		m.calendarReport(w, r, username, userinfo)
	case "MKCALENDAR":
		http.Error(w, "Calendar already exists", http.StatusMethodNotAllowed)
	default:
		w.Header().Set("Allow", "OPTIONS, PROPFIND, REPORT, MKCALENDAR")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (m *Manager) calendarPropfind(w http.ResponseWriter, r *http.Request, username string, userinfo *user.User) {
	calHref := "/caldav/" + username + "/notes/"
	notes, _ := m.loadNotes(userinfo)
	token := syncToken(notes)

	calProps := xmlPropstat(http.StatusOK,
		`<resourcetype><collection/><C:calendar xmlns:C="urn:ietf:params:xml:ns:caldav"/></resourcetype>`,
		`<displayname>Notes</displayname>`,
		`<CS:getctag xmlns:CS="http://calendarserver.org/ns/">`+token+`</CS:getctag>`,
		`<sync-token>`+xmlEsc(calHref+"sync/"+token)+`</sync-token>`,
		`<C:supported-calendar-component-set xmlns:C="urn:ietf:params:xml:ns:caldav"><C:comp name="VTODO"/></C:supported-calendar-component-set>`,
		`<C:calendar-description xmlns:C="urn:ietf:params:xml:ns:caldav">ArozOS Notes</C:calendar-description>`,
	)

	depth := r.Header.Get("Depth")
	responses := xmlResponse(calHref, calProps)

	if depth == "1" || depth == "infinity" {
		for _, n := range notes {
			content, ts := m.readNoteContent(userinfo, n.ID)
			etag := noteETag(content)
			itemHref := calHref + n.ID + ".ics"
			responses += "\n" + xmlResponse(itemHref,
				xmlPropstat(http.StatusOK,
					`<resourcetype/>`,
					`<getetag>"`+etag+`"</getetag>`,
					`<getcontenttype>text/calendar; charset=utf-8; component=VTODO</getcontenttype>`,
					`<displayname>`+xmlEsc(n.Title)+`</displayname>`,
					`<C:calendar-data xmlns:C="urn:ietf:params:xml:ns:caldav">`+xmlEsc(noteToIcal(n.ID, content, ts))+`</C:calendar-data>`,
				),
			)
		}
	}

	writeXML(w, http.StatusMultiStatus, xmlMultistatus(responses))
}

func (m *Manager) calendarReport(w http.ResponseWriter, r *http.Request, username string, userinfo *user.User) {
	body, _ := io.ReadAll(r.Body)
	bodyStr := string(body)

	// Distinguish report type from the root element name.
	if strings.Contains(bodyStr, "sync-collection") {
		m.reportSyncCollection(w, r, username, userinfo, bodyStr)
	} else {
		// Treat everything else as calendar-query / calendar-multiget.
		m.reportCalendarQuery(w, r, username, userinfo, bodyStr)
	}
}

func (m *Manager) reportCalendarQuery(w http.ResponseWriter, r *http.Request, username string, userinfo *user.User, body string) {
	calHref := "/caldav/" + username + "/notes/"
	notes, _ := m.loadNotes(userinfo)

	// calendar-multiget: client lists explicit hrefs.
	wantedIDs := parseMultigetHrefs(body, calHref)

	responses := ""
	for _, n := range notes {
		if len(wantedIDs) > 0 && !wantedIDs[n.ID] {
			continue
		}
		content, ts := m.readNoteContent(userinfo, n.ID)
		etag := noteETag(content)
		itemHref := calHref + n.ID + ".ics"
		responses += xmlResponse(itemHref,
			xmlPropstat(http.StatusOK,
				`<getetag>"`+etag+`"</getetag>`,
				`<C:calendar-data xmlns:C="urn:ietf:params:xml:ns:caldav">`+xmlEsc(noteToIcal(n.ID, content, ts))+`</C:calendar-data>`,
			),
		)
	}

	writeXML(w, http.StatusMultiStatus, xmlMultistatus(responses))
}

func (m *Manager) reportSyncCollection(w http.ResponseWriter, r *http.Request, username string, userinfo *user.User, body string) {
	// We always return the full set (stateless sync-token).
	m.reportCalendarQuery(w, r, username, userinfo, body)
}

// handleItem handles GET, PUT, DELETE on a single note .ics resource.
func (m *Manager) handleItem(w http.ResponseWriter, r *http.Request, username, noteID string, userinfo *user.User) {
	calHref := "/caldav/" + username + "/notes/"
	itemHref := calHref + noteID + ".ics"

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		content, ts := m.readNoteContent(userinfo, noteID)
		if content == "" && ts == 0 {
			http.NotFound(w, r)
			return
		}
		ical := noteToIcal(noteID, content, ts)
		etag := noteETag(content)
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("ETag", `"`+etag+`"`)
		w.Header().Set("Content-Location", itemHref)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, ical)

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}
		summary, description, _ := parseVTODO(string(body))

		// The note content is the description; fall back to summary.
		content := description
		if strings.TrimSpace(content) == "" {
			content = summary
		}

		ts := time.Now().UnixMilli()
		if err := m.writeNote(userinfo, noteID, content, ts); err != nil {
			http.Error(w, "Failed to write note: "+err.Error(), http.StatusInternalServerError)
			return
		}
		etag := noteETag(content)
		w.Header().Set("ETag", `"`+etag+`"`)
		w.Header().Set("Location", itemHref)
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		if err := m.deleteNote(userinfo, noteID); err != nil {
			http.Error(w, "Failed to delete note: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case "PROPFIND":
		content, ts := m.readNoteContent(userinfo, noteID)
		if content == "" && ts == 0 {
			http.NotFound(w, r)
			return
		}
		etag := noteETag(content)
		n := noteMeta{ID: noteID}
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				n.Title = line
				if len(n.Title) > 60 {
					n.Title = n.Title[:60]
				}
				break
			}
		}
		body := xmlMultistatus(
			xmlResponse(itemHref,
				xmlPropstat(http.StatusOK,
					`<resourcetype/>`,
					`<getetag>"`+etag+`"</getetag>`,
					`<getcontenttype>text/calendar; charset=utf-8; component=VTODO</getcontenttype>`,
					`<displayname>`+xmlEsc(n.Title)+`</displayname>`,
				),
			),
		)
		writeXML(w, http.StatusMultiStatus, body)

	default:
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Notes filesystem helpers
// ─────────────────────────────────────────────────────────────────────────────

// loadNotes reads meta.json and returns the list of notes.
func (m *Manager) loadNotes(userinfo *user.User) ([]noteMeta, error) {
	handler, err := userinfo.GetFileSystemHandlerFromVirtualPath("user:/Document/Notes")
	if err != nil {
		return nil, err
	}
	metaReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/meta.json", userinfo.Username)
	if err != nil {
		return nil, err
	}
	raw, err := handler.FileSystemAbstraction.ReadFile(metaReal)
	if err != nil {
		return []noteMeta{}, nil // no notes yet
	}
	var meta notesMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return []noteMeta{}, nil
	}
	return meta.Notes, nil
}

// readNoteContent reads a note's text content and its last-modified timestamp (ms).
func (m *Manager) readNoteContent(userinfo *user.User, id string) (content string, ts int64) {
	handler, err := userinfo.GetFileSystemHandlerFromVirtualPath("user:/Document/Notes")
	if err != nil {
		return "", 0
	}
	realPath, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/"+id+".txt", userinfo.Username)
	if err != nil {
		return "", 0
	}
	raw, err := handler.FileSystemAbstraction.ReadFile(realPath)
	if err != nil {
		return "", 0
	}
	fi, err := handler.FileSystemAbstraction.Stat(realPath)
	if err == nil {
		ts = fi.ModTime().UnixMilli()
	} else {
		ts = time.Now().UnixMilli()
	}
	return string(raw), ts
}

// writeNote writes note content and updates meta.json.
func (m *Manager) writeNote(userinfo *user.User, id, content string, ts int64) error {
	handler, err := userinfo.GetFileSystemHandlerFromVirtualPath("user:/Document/Notes")
	if err != nil {
		return err
	}

	// Ensure notes directory exists.
	dirReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes", userinfo.Username)
	if err != nil {
		return err
	}
	handler.FileSystemAbstraction.MkdirAll(dirReal, 0755)

	// Write note file.
	noteReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/"+id+".txt", userinfo.Username)
	if err != nil {
		return err
	}
	if err := handler.FileSystemAbstraction.WriteFile(noteReal, []byte(content), 0644); err != nil {
		return err
	}

	// Update meta.json.
	metaReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/meta.json", userinfo.Username)
	if err != nil {
		return err
	}
	var meta notesMeta
	if raw, err := handler.FileSystemAbstraction.ReadFile(metaReal); err == nil {
		json.Unmarshal(raw, &meta)
	}
	if meta.Notes == nil {
		meta.Notes = []noteMeta{}
	}

	title := extractTitle(content)
	found := false
	for i, n := range meta.Notes {
		if n.ID == id {
			meta.Notes[i].Title = title
			meta.Notes[i].UpdatedAt = ts
			found = true
			break
		}
	}
	if !found {
		meta.Notes = append(meta.Notes, noteMeta{ID: id, Title: title, UpdatedAt: ts})
	}
	meta.LastOpened = id

	raw, _ := json.Marshal(meta)
	return handler.FileSystemAbstraction.WriteFile(metaReal, raw, 0644)
}

// deleteNote removes a note file and updates meta.json.
func (m *Manager) deleteNote(userinfo *user.User, id string) error {
	handler, err := userinfo.GetFileSystemHandlerFromVirtualPath("user:/Document/Notes")
	if err != nil {
		return err
	}
	noteReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/"+id+".txt", userinfo.Username)
	if err != nil {
		return err
	}
	_ = handler.FileSystemAbstraction.Remove(noteReal)

	// Update meta.json.
	metaReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/meta.json", userinfo.Username)
	if err != nil {
		return err
	}
	var meta notesMeta
	if raw, err := handler.FileSystemAbstraction.ReadFile(metaReal); err == nil {
		json.Unmarshal(raw, &meta)
	}
	newNotes := []noteMeta{}
	for _, n := range meta.Notes {
		if n.ID != id {
			newNotes = append(newNotes, n)
		}
	}
	meta.Notes = newNotes
	if meta.LastOpened == id {
		if len(meta.Notes) > 0 {
			meta.LastOpened = meta.Notes[0].ID
		} else {
			meta.LastOpened = ""
		}
	}

	raw, _ := json.Marshal(meta)
	return handler.FileSystemAbstraction.WriteFile(metaReal, raw, 0644)
}

// ─────────────────────────────────────────────────────────────────────────────
// iCalendar helpers
// ─────────────────────────────────────────────────────────────────────────────

// noteToIcal converts a note into an iCalendar VCALENDAR/VTODO string.
func noteToIcal(id, content string, tsMs int64) string {
	if tsMs == 0 {
		tsMs = time.Now().UnixMilli()
	}
	t := time.UnixMilli(tsMs).UTC()
	stamp := time.Now().UTC().Format("20060102T150405Z")
	modified := t.Format("20060102T150405Z")
	title := extractTitle(content)

	var sb strings.Builder
	sb.WriteString("BEGIN:VCALENDAR\r\n")
	sb.WriteString("VERSION:2.0\r\n")
	sb.WriteString("PRODID:" + prodID + "\r\n")
	sb.WriteString("BEGIN:VTODO\r\n")
	icalWriteProp(&sb, "UID", id+"@arozos")
	icalWriteProp(&sb, "DTSTAMP", stamp)
	icalWriteProp(&sb, "LAST-MODIFIED", modified)
	icalWriteProp(&sb, "CREATED", modified)
	icalWriteProp(&sb, "SUMMARY", icalEscape(title))
	icalWriteProp(&sb, "DESCRIPTION", icalEscape(content))
	sb.WriteString("STATUS:NEEDS-ACTION\r\n")
	sb.WriteString("END:VTODO\r\n")
	sb.WriteString("END:VCALENDAR\r\n")
	return sb.String()
}

// icalWriteProp writes a single iCalendar property with line folding at 75 octets.
func icalWriteProp(sb *strings.Builder, name, value string) {
	line := name + ":" + value
	for len(line) > 75 {
		sb.WriteString(line[:75] + "\r\n ")
		line = line[75:]
	}
	sb.WriteString(line + "\r\n")
}

// icalEscape escapes special characters for iCalendar TEXT values.
func icalEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\r\n", `\n`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\n`)
	return s
}

// icalUnescape reverses iCalendar TEXT escaping.
func icalUnescape(s string) string {
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\N`, "\n")
	s = strings.ReplaceAll(s, `\,`, ",")
	s = strings.ReplaceAll(s, `\;`, ";")
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}

// parseVTODO extracts SUMMARY, DESCRIPTION, and UID from an iCalendar string.
func parseVTODO(data string) (summary, description, uid string) {
	// Unfold continuation lines (CRLF/LF + whitespace).
	data = strings.ReplaceAll(data, "\r\n ", "")
	data = strings.ReplaceAll(data, "\r\n\t", "")
	data = strings.ReplaceAll(data, "\n ", "")
	data = strings.ReplaceAll(data, "\n\t", "")

	inTodo := false
	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch line {
		case "BEGIN:VTODO":
			inTodo = true
			continue
		case "END:VTODO":
			return
		}
		if !inTodo {
			continue
		}
		// Property name may have parameters (PROPERTY;param=value:value).
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		propName := strings.ToUpper(line[:colonIdx])
		// Strip parameters (DESCRIPTION;LANGUAGE=en → DESCRIPTION).
		if semi := strings.Index(propName, ";"); semi >= 0 {
			propName = propName[:semi]
		}
		val := icalUnescape(line[colonIdx+1:])
		switch propName {
		case "SUMMARY":
			summary = val
		case "DESCRIPTION":
			description = val
		case "UID":
			uid = strings.TrimSuffix(val, "@arozos")
		}
	}
	return
}

// ─────────────────────────────────────────────────────────────────────────────
// XML helpers
// ─────────────────────────────────────────────────────────────────────────────

func writeXML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprint(w, body)
}

func xmlMultistatus(responses string) string {
	return `<multistatus xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/">` +
		responses +
		`</multistatus>`
}

func xmlResponse(href string, propstats ...string) string {
	return `<response><href>` + xmlEsc(href) + `</href>` +
		strings.Join(propstats, "") +
		`</response>`
}

func xmlPropstat(status int, props ...string) string {
	statusLine := "HTTP/1.1 " + strconv.Itoa(status) + " " + http.StatusText(status)
	return `<propstat><prop>` +
		strings.Join(props, "") +
		`</prop><status>` + statusLine + `</status></propstat>`
}

// xmlEsc escapes characters that are special in XML.
func xmlEsc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// ─────────────────────────────────────────────────────────────────────────────
// Utilities
// ─────────────────────────────────────────────────────────────────────────────

var validIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func validID(id string) bool {
	return len(id) > 0 && validIDRe.MatchString(id)
}

func extractTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 60 {
				line = line[:60]
			}
			return line
		}
	}
	return "Untitled"
}

// noteETag returns a short hex digest of the note content.
func noteETag(content string) string {
	h := md5.Sum([]byte(content))
	return hex.EncodeToString(h[:8])
}

// syncToken returns a token representing the current state of all notes.
func syncToken(notes []noteMeta) string {
	// Sort for stable ordering.
	sorted := make([]noteMeta, len(notes))
	copy(sorted, notes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	h := md5.New()
	for _, n := range sorted {
		fmt.Fprintf(h, "%s:%d|", n.ID, n.UpdatedAt)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// calCTag returns a coarse change tag based on current time (minute granularity).
func calCTag() string {
	return strconv.FormatInt(time.Now().Unix()/60, 10)
}

// parseMultigetHrefs extracts note IDs from a calendar-multiget request body.
func parseMultigetHrefs(body, calPathPrefix string) map[string]bool {
	result := map[string]bool{}
	// Find all <href>...</href> occurrences.
	re := regexp.MustCompile(`<[^>]*href[^>]*>([^<]+)</[^>]*href>`)
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		href := strings.TrimSpace(m[1])
		if strings.HasPrefix(href, calPathPrefix) && strings.HasSuffix(href, ".ics") {
			id := strings.TrimSuffix(strings.TrimPrefix(href, calPathPrefix), ".ics")
			if validID(id) {
				result[id] = true
			}
		}
	}
	return result
}
