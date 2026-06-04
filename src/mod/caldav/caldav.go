package caldav

/*
	CalDAV Server Module for ArozOS
	Provides RFC 4791 CalDAV access to Notes app data as VTODO items.

	Authentication: HTTP Basic Auth
	  - Username: arozos username
	  - Password: auto-login token (obtain from My Account → Security)

	Discovery chain (3-step, tested to work with Apple iOS):

	  PROPFIND /caldav/                       → current-user-principal = /caldav/principals/{user}/
	  PROPFIND /caldav/principals/{user}/     → calendar-home-set = /caldav/{user}/
	  PROPFIND /caldav/{user}/ Depth:1        → Notes calendar listed
	  REPORT   /caldav/{user}/notes/          → sync VTODO items
	  GET/PUT/DELETE /caldav/{user}/notes/{id}.ics → individual notes

	The principal URL (/caldav/principals/{user}/) MUST be different from
	the calendar home (/caldav/{user}/). iOS rejects the account when
	calendar-home-set points to the same URL as the principal (circular ref).

	No non-stdlib / non-commercial dependencies are introduced.
*/

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
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
	backend     caldavUserHandler // internal: testable via interface
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

// NewManager creates a Manager backed by the real arozos user stack.
func NewManager(userHandler *user.UserHandler, database *db.Database) *Manager {
	database.NewTable("caldav") // create bucket if it doesn't exist yet
	enabled := false
	database.Read("caldav", "enabled", &enabled)
	return &Manager{
		UserHandler: userHandler,
		Database:    database,
		Enabled:     enabled,
		backend:     &arozosBackend{h: userHandler},
	}
}

// newManagerWithBackend creates a Manager with a custom backend (for tests).
func newManagerWithBackend(backend caldavUserHandler, enabled bool) *Manager {
	return &Manager{Enabled: enabled, backend: backend}
}

// HandleToggle is an admin-only endpoint to enable/disable CalDAV.
func (m *Manager) HandleToggle(w http.ResponseWriter, r *http.Request) {
	state := r.FormValue("enable")
	m.Enabled = state == "true"
	if m.Database != nil {
		m.Database.Write("caldav", "enabled", m.Enabled)
	}
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
	// Log every incoming request with all headers for diagnostics.
	log.Printf("[CalDAV] ← %s %s (from %s)", r.Method, r.URL.RequestURI(), r.RemoteAddr)
	for name, vals := range r.Header {
		log.Printf("[CalDAV]   header %s: %s", name, strings.Join(vals, "; "))
	}

	// Log request body for PROPFIND/REPORT so we can see what properties the client wants.
	// Read it here and restore via strings.Reader so downstream handlers still see the body.
	if r.Method == "PROPFIND" || r.Method == "REPORT" {
		if bodyBytes, err := io.ReadAll(r.Body); err == nil {
			if len(bodyBytes) > 0 {
				preview := string(bodyBytes)
				if len(preview) > 600 {
					preview = preview[:600] + "…"
				}
				log.Printf("[CalDAV]   body (%d bytes): %s", len(bodyBytes), preview)
			}
			r.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
		}
	}

	if !m.Enabled {
		log.Printf("[CalDAV] service is disabled — rejecting %s %s", r.Method, r.URL.Path)
		http.Error(w, "CalDAV service is disabled", http.StatusServiceUnavailable)
		return
	}

	// Advertise DAV capabilities on every response.
	w.Header().Set("DAV", "1, 2, calendar-access")

	if r.Method == http.MethodOptions {
		log.Printf("[CalDAV] OPTIONS %s — returning capabilities (no auth required)", r.URL.Path)
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, REPORT, MKCALENDAR")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Strip prefix early so we can give the root PROPFIND a free pass.
	reqPath := strings.TrimPrefix(r.URL.Path, "/caldav")
	if reqPath == "" {
		reqPath = "/"
	}

	// Root PROPFIND is deliberately unauthenticated.
	// Apple accountsd (iOS/macOS) probes the server root without credentials first.
	// It expects a 207 that points to current-user-principal, then authenticates
	// only when it follows that URL. Returning 401 here causes accountsd to give
	// up without ever retrying with the user's credentials.
	if reqPath == "/" && r.Method == "PROPFIND" {
		log.Printf("[CalDAV] → unauthenticated root discovery (auth not required for probe)")
		m.handleRootDiscovery(w, r)
		return
	}

	// Basic Auth: username + auto-login token.
	username, token, ok := r.BasicAuth()
	if !ok {
		authHdr := r.Header.Get("Authorization")
		if authHdr == "" {
			log.Printf("[CalDAV] %s %s — no Authorization header at all, returning 401 (iOS should retry with credentials)", r.Method, r.URL.Path)
		} else {
			// Header present but not parseable as Basic Auth — log first 20 chars safely
			preview := authHdr
			if len(preview) > 20 {
				preview = preview[:20] + "..."
			}
			log.Printf("[CalDAV] %s %s — Authorization header present but not valid Basic Auth (value: %q), returning 401", r.Method, r.URL.Path, preview)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="ArozOS CalDAV"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	log.Printf("[CalDAV] %s %s — auth attempt for user %q", r.Method, r.URL.Path, username)

	authAgent := m.backend.GetAuthAgent()
	valid, tokenOwner := authAgent.ValidateAutoLoginToken(token)
	if !valid {
		log.Printf("[CalDAV] auth FAILED for user %q — token not recognised (token len=%d)", username, len(token))
		w.Header().Set("WWW-Authenticate", `Basic realm="ArozOS CalDAV"`)
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	if tokenOwner != username {
		log.Printf("[CalDAV] auth FAILED — token belongs to %q but request claimed user %q", tokenOwner, username)
		w.Header().Set("WWW-Authenticate", `Basic realm="ArozOS CalDAV"`)
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	log.Printf("[CalDAV] auth OK for user %q", username)

	userinfo, err := m.backend.GetUser(username)
	if err != nil {
		log.Printf("[CalDAV] user lookup failed for %q: %v", username, err)
		http.Error(w, "User not found", http.StatusUnauthorized)
		return
	}

	// Strip /caldav prefix; normalise to always start with "/".
	path := strings.TrimPrefix(r.URL.Path, "/caldav")
	if path == "" {
		path = "/"
	}

	log.Printf("[CalDAV] routing %s %s (path=%s, user=%s)", r.Method, r.URL.Path, path, username)
	m.route(w, r, path, username, userinfo)
}

// ─────────────────────────────────────────────────────────────────────────────
// Routing
// ─────────────────────────────────────────────────────────────────────────────

func (m *Manager) route(w http.ResponseWriter, r *http.Request, path, username string, userinfo caldavUser) {
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
		log.Printf("[CalDAV] → discovery fallback handler (non-PROPFIND on root)")
		m.handleDiscovery(w, r, username)
	case normPath == "/current-user-principal/":
		log.Printf("[CalDAV] → current-user-principal handler")
		m.handleCurrentUserPrincipal(w, r, username)
	case normPath == principalPath:
		log.Printf("[CalDAV] → principal handler")
		m.handlePrincipal(w, r, username)
	case normPath == homePath:
		log.Printf("[CalDAV] → calendar home handler (Depth: %q)", r.Header.Get("Depth"))
		m.handleCalendarHome(w, r, username, userinfo)
	case normPath == calPath:
		log.Printf("[CalDAV] → notes calendar handler")
		m.handleCalendar(w, r, username, userinfo)
	case strings.HasPrefix(normPath, calPath) && strings.HasSuffix(path, ".ics"):
		noteID := strings.TrimSuffix(strings.TrimPrefix(path, calPath), ".ics")
		if !validID(noteID) {
			log.Printf("[CalDAV] invalid note ID %q in path %s", noteID, path)
			http.Error(w, "Invalid note ID", http.StatusBadRequest)
			return
		}
		log.Printf("[CalDAV] → item handler (noteID=%s)", noteID)
		m.handleItem(w, r, username, noteID, userinfo)
	default:
		log.Printf("[CalDAV] no route matched for path=%q (normPath=%q, principalPath=%q, homePath=%q, calPath=%q)",
			path, normPath, principalPath, homePath, calPath)
		http.NotFound(w, r)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CalDAV resource handlers
// ─────────────────────────────────────────────────────────────────────────────

// handleRootDiscovery: unauthenticated PROPFIND /caldav/ → fixed current-user-principal.
// Apple accountsd probes the server root WITHOUT credentials. Returning 401 here causes
// accountsd to give up entirely — it will never retry with credentials. Instead we return
// a generic /caldav/current-user-principal/ URL. accountsd follows it, gets 401 there
// (auth IS required), then retries with credentials — at which point we know the username.
//
// RFC 4918 §9.1: requested properties that don't exist on this resource MUST be returned
// in a separate <propstat> with status 404. Apple clients restart discovery instead of
// proceeding when expected propstats are absent.
func (m *Manager) handleRootDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PROPFIND" {
		w.Header().Set("Allow", "OPTIONS, PROPFIND")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	body := xmlMS(
		xmlResp("/caldav/",
			// Properties the root collection DOES have:
			xmlPS(http.StatusOK,
				`<current-user-principal><href>/caldav/current-user-principal/</href></current-user-principal>`,
				`<resourcetype><collection/></resourcetype>`,
				`<displayname>ArozOS CalDAV</displayname>`,
			),
			// Root is not a principal — explicit 404 for all principal/calendar props
			// Apple clients commonly request (omitting them triggers a discovery restart):
			xmlPS(http.StatusNotFound,
				`<principal-URL/>`,
				`<principal-collection-set/>`,
				`<C:calendar-home-set/>`,
				`<C:calendar-user-address-set/>`,
				`<CS:dropbox-home-URL/>`,
				`<CS:email-address-set/>`,
				`<CS:notification-URL/>`,
				`<C:max-attendees-per-instance/>`,
				`<C:schedule-inbox-URL/>`,
				`<C:schedule-outbox-URL/>`,
			),
		),
	)
	writeXML(w, http.StatusMultiStatus, body)
}

// handleCurrentUserPrincipal: PROPFIND /caldav/current-user-principal/ → calendar-home-set.
// accountsd follows the URL from handleRootDiscovery, gets 401, then retries with credentials.
// At that point we know the username and can return the user-specific calendar home URL.
func (m *Manager) handleCurrentUserPrincipal(w http.ResponseWriter, r *http.Request, username string) {
	if r.Method != "PROPFIND" {
		w.Header().Set("Allow", "OPTIONS, PROPFIND")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	principalHref := "/caldav/current-user-principal/"
	homeHref := "/caldav/" + username + "/"
	body := xmlMS(
		xmlResp(principalHref,
			// Properties this principal resource HAS:
			xmlPS(http.StatusOK,
				`<displayname>`+xmlEsc(username)+`</displayname>`,
				`<resourcetype><collection/><principal/></resourcetype>`,
				`<current-user-principal><href>`+xmlEsc(principalHref)+`</href></current-user-principal>`,
				// principal-URL: self-referential per RFC 3744.
				// Apple clients explicitly request this; omitting it causes a discovery restart.
				`<principal-URL><href>`+xmlEsc(principalHref)+`</href></principal-URL>`,
				`<principal-collection-set><href>/caldav/principals/</href></principal-collection-set>`,
				`<C:calendar-home-set><href>`+xmlEsc(homeHref)+`</href></C:calendar-home-set>`,
				`<C:calendar-user-address-set><href>mailto:`+xmlEsc(username)+`@arozos.local</href></C:calendar-user-address-set>`,
			),
			// Apple CalendarServer-specific properties we don't implement — explicit 404
			// so clients know these don't exist (RFC 4918 §9.1):
			xmlPS(http.StatusNotFound,
				`<CS:dropbox-home-URL/>`,
				`<CS:email-address-set/>`,
				`<CS:notification-URL/>`,
				`<C:max-attendees-per-instance/>`,
				`<C:schedule-inbox-URL/>`,
				`<C:schedule-outbox-URL/>`,
			),
		),
	)
	writeXML(w, http.StatusMultiStatus, body)
}

// handleDiscovery: fallback for authenticated requests to /caldav/ (non-PROPFIND methods).
// PROPFIND is intercepted earlier in HandleRequest and served without auth.
func (m *Manager) handleDiscovery(w http.ResponseWriter, r *http.Request, username string) {
	w.Header().Set("Allow", "OPTIONS, PROPFIND")
	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

// handlePrincipal: PROPFIND /caldav/principals/{user}/ → calendar-home-set (step 2).
// The principal URL MUST differ from the calendar home; iOS rejects accounts
// where calendar-home-set === principal URL.
func (m *Manager) handlePrincipal(w http.ResponseWriter, r *http.Request, username string) {
	if r.Method != "PROPFIND" {
		w.Header().Set("Allow", "OPTIONS, PROPFIND")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	principalHref := "/caldav/principals/" + username + "/"
	homeHref := "/caldav/" + username + "/"
	body := xmlMS(
		xmlResp(principalHref,
			xmlPS(http.StatusOK,
				`<displayname>`+xmlEsc(username)+`</displayname>`,
				`<principal-URL><href>`+xmlEsc(principalHref)+`</href></principal-URL>`,
				`<principal-collection-set><href>/caldav/principals/</href></principal-collection-set>`,
				`<resourcetype><collection/><principal/></resourcetype>`,
				`<current-user-principal><href>`+xmlEsc(principalHref)+`</href></current-user-principal>`,
				`<C:calendar-home-set><href>`+xmlEsc(homeHref)+`</href></C:calendar-home-set>`,
				`<C:calendar-user-address-set><href>mailto:`+xmlEsc(username)+`@arozos.local</href></C:calendar-user-address-set>`,
			),
			xmlPS(http.StatusNotFound,
				`<CS:dropbox-home-URL/>`,
				`<CS:email-address-set/>`,
				`<CS:notification-URL/>`,
				`<C:max-attendees-per-instance/>`,
				`<C:schedule-inbox-URL/>`,
				`<C:schedule-outbox-URL/>`,
			),
		),
	)
	writeXML(w, http.StatusMultiStatus, body)
}

// handleCalendarHome: PROPFIND /caldav/{user}/ → list calendars when Depth != "0".
func (m *Manager) handleCalendarHome(w http.ResponseWriter, r *http.Request, username string, userinfo caldavUser) {
	if r.Method != "PROPFIND" {
		w.Header().Set("Allow", "OPTIONS, PROPFIND")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	homeHref := "/caldav/" + username + "/"
	calHref := "/caldav/" + username + "/notes/"

	homeResp := xmlResp(homeHref,
		xmlPS(http.StatusOK,
			`<resourcetype><collection/></resourcetype>`,
			`<displayname>`+xmlEsc(username)+`</displayname>`,
		),
	)

	calResp := ""
	if r.Header.Get("Depth") != "0" { // default (no header) = infinity per RFC 4918
		notes, _ := userinfo.LoadNotes()
		token := syncToken(notes)
		calResp = "\n" + xmlResp(calHref,
			xmlPS(http.StatusOK,
				`<resourcetype><collection/><C:calendar/></resourcetype>`,
				`<displayname>Notes</displayname>`,
				`<CS:getctag>`+token+`</CS:getctag>`,
				`<sync-token>`+xmlEsc("urn:arozos:caldav:"+token)+`</sync-token>`,
				`<C:supported-calendar-component-set><C:comp name="VTODO"/></C:supported-calendar-component-set>`,
				`<C:calendar-description>ArozOS Notes</C:calendar-description>`,
				`<IC:calendar-color>#0082FC</IC:calendar-color>`,
				`<supported-report-set>`+
					`<supported-report><report><sync-collection/></report></supported-report>`+
					`<supported-report><report><C:calendar-query/></report></supported-report>`+
					`<supported-report><report><C:calendar-multiget/></report></supported-report>`+
					`</supported-report-set>`,
			),
		)
	}

	writeXML(w, http.StatusMultiStatus, xmlMS(homeResp+calResp))
}

// handleCalendar dispatches on the notes calendar collection.
func (m *Manager) handleCalendar(w http.ResponseWriter, r *http.Request, username string, userinfo caldavUser) {
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

func (m *Manager) calendarPropfind(w http.ResponseWriter, r *http.Request, username string, userinfo caldavUser) {
	calHref := "/caldav/" + username + "/notes/"
	notes, _ := userinfo.LoadNotes()
	token := syncToken(notes)

	calProps := xmlPS(http.StatusOK,
		`<resourcetype><collection/><C:calendar/></resourcetype>`,
		`<displayname>Notes</displayname>`,
		`<CS:getctag>`+token+`</CS:getctag>`,
		`<sync-token>`+xmlEsc("urn:arozos:caldav:"+token)+`</sync-token>`,
		`<C:supported-calendar-component-set><C:comp name="VTODO"/></C:supported-calendar-component-set>`,
		`<C:calendar-description>ArozOS Notes</C:calendar-description>`,
		`<IC:calendar-color>#0082FC</IC:calendar-color>`,
		// RFC 6578: servers that support sync-collection MUST advertise it here.
		`<supported-report-set>`+
			`<supported-report><report><sync-collection/></report></supported-report>`+
			`<supported-report><report><C:calendar-query/></report></supported-report>`+
			`<supported-report><report><C:calendar-multiget/></report></supported-report>`+
			`</supported-report-set>`,
	)
	responses := xmlResp(calHref, calProps)

	depth := r.Header.Get("Depth")
	if depth == "1" || depth == "infinity" {
		for _, n := range notes {
			content, ts := userinfo.ReadNoteContent(n.ID)
			etag := noteETag(content)
			itemHref := calHref + n.ID + ".ics"
			responses += "\n" + xmlResp(itemHref,
				xmlPS(http.StatusOK,
					`<resourcetype/>`,
					`<getetag>"`+etag+`"</getetag>`,
					`<getcontenttype>text/calendar; charset=utf-8</getcontenttype>`,
					`<displayname>`+xmlEsc(n.Title)+`</displayname>`,
					`<C:calendar-data>`+xmlEsc(noteToIcal(n.ID, content, ts))+`</C:calendar-data>`,
				),
			)
		}
	}

	writeXML(w, http.StatusMultiStatus, xmlMS(responses))
}

func (m *Manager) calendarReport(w http.ResponseWriter, r *http.Request, username string, userinfo caldavUser) {
	body, _ := io.ReadAll(r.Body)
	bodyStr := string(body)
	if strings.Contains(bodyStr, "sync-collection") {
		m.reportSyncCollection(w, r, username, userinfo)
	} else {
		m.reportCalendarQuery(w, r, username, userinfo, bodyStr)
	}
}

// reportSyncCollection handles RFC 6578 sync-collection REPORT.
// Apple clients (remindd) use this to do initial and incremental sync.
// The response must:
//  1. Include the collection itself as the first <response>.
//  2. Include each item with getcontenttype + getetag ONLY (no calendar-data).
//  3. End with a <sync-token> element inside <multistatus>.
//
// A follow-up calendar-multiget REPORT fetches the actual iCal data.
func (m *Manager) reportSyncCollection(w http.ResponseWriter, r *http.Request, username string, userinfo caldavUser) {
	calHref := "/caldav/" + username + "/notes/"
	notes, _ := userinfo.LoadNotes()
	token := syncToken(notes)

	// First response: the collection itself.
	responses := xmlResp(calHref,
		xmlPS(http.StatusOK,
			`<getcontenttype>httpd/unix-directory</getcontenttype>`,
			`<getetag>"`+token+`"</getetag>`,
		),
	)

	// One response per item — etag + content-type only.
	for _, n := range notes {
		content, _ := userinfo.ReadNoteContent(n.ID)
		etag := noteETag(content)
		itemHref := calHref + n.ID + ".ics"
		responses += xmlResp(itemHref,
			xmlPS(http.StatusOK,
				`<getcontenttype>text/calendar; charset=utf-8</getcontenttype>`,
				`<getetag>"`+etag+`"</getetag>`,
			),
		)
	}

	writeXML(w, http.StatusMultiStatus,
		xmlMSSync(responses, "urn:arozos:caldav:"+token))
}

// reportCalendarQuery handles calendar-query and calendar-multiget REPORTs.
// It returns full calendar-data for each matching item.
func (m *Manager) reportCalendarQuery(w http.ResponseWriter, r *http.Request, username string, userinfo caldavUser, body string) {
	calHref := "/caldav/" + username + "/notes/"
	notes, _ := userinfo.LoadNotes()
	wantedIDs := parseMultigetHrefs(body, calHref)

	responses := ""
	for _, n := range notes {
		if len(wantedIDs) > 0 && !wantedIDs[n.ID] {
			continue
		}
		content, ts := userinfo.ReadNoteContent(n.ID)
		etag := noteETag(content)
		itemHref := calHref + n.ID + ".ics"
		responses += xmlResp(itemHref,
			xmlPS(http.StatusOK,
				`<getetag>"`+etag+`"</getetag>`,
				`<C:calendar-data>`+xmlEsc(noteToIcal(n.ID, content, ts))+`</C:calendar-data>`,
			),
		)
	}

	token := syncToken(notes)
	writeXML(w, http.StatusMultiStatus,
		xmlMSSync(responses, "urn:arozos:caldav:"+token))
}

// handleItem handles GET/HEAD/PUT/DELETE/PROPFIND on a single .ics resource.
func (m *Manager) handleItem(w http.ResponseWriter, r *http.Request, username, noteID string, userinfo caldavUser) {
	calHref := "/caldav/" + username + "/notes/"
	itemHref := calHref + noteID + ".ics"

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		content, ts := userinfo.ReadNoteContent(noteID)
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
		_, description, uid := parseVTODO(string(body))
		if uid != "" && uid != noteID && validID(uid) {
			noteID = uid
		}
		content := strings.TrimSpace(description)
		if content == "" {
			content = strings.TrimSpace(string(body))
		}
		ts := time.Now().UnixMilli()
		if err := userinfo.WriteNote(noteID, content, ts); err != nil {
			http.Error(w, "Failed to write note: "+err.Error(), http.StatusInternalServerError)
			return
		}
		etag := noteETag(content)
		w.Header().Set("ETag", `"`+etag+`"`)
		w.Header().Set("Location", calHref+noteID+".ics")
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		if err := userinfo.DeleteNote(noteID); err != nil {
			http.Error(w, "Failed to delete note: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case "PROPFIND":
		content, ts := userinfo.ReadNoteContent(noteID)
		if content == "" && ts == 0 {
			http.NotFound(w, r)
			return
		}
		etag := noteETag(content)
		body := xmlMS(
			xmlResp(itemHref,
				xmlPS(http.StatusOK,
					`<resourcetype/>`,
					`<getetag>"`+etag+`"</getetag>`,
					`<getcontenttype>text/calendar; charset=utf-8</getcontenttype>`,
					`<displayname>`+xmlEsc(extractTitle(content))+`</displayname>`,
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
// iCalendar helpers
// ─────────────────────────────────────────────────────────────────────────────

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
	sb.WriteString("CALSCALE:GREGORIAN\r\n") // required by Apple clients
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

func icalWriteProp(sb *strings.Builder, name, value string) {
	line := name + ":" + value
	// RFC 5545: first segment ≤75 octets; continuation lines start with one
	// space, so their content portion is ≤74 octets (75 total incl. space).
	if len(line) > 75 {
		sb.WriteString(line[:75] + "\r\n ")
		line = line[75:]
		for len(line) > 74 {
			sb.WriteString(line[:74] + "\r\n ")
			line = line[74:]
		}
	}
	sb.WriteString(line + "\r\n")
}

func icalEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\r\n", `\n`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\n`)
	return s
}

func icalUnescape(s string) string {
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\N`, "\n")
	s = strings.ReplaceAll(s, `\,`, ",")
	s = strings.ReplaceAll(s, `\;`, ";")
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}

func parseVTODO(data string) (summary, description, uid string) {
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
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		propName := strings.ToUpper(line[:colonIdx])
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
// XML helpers — default DAV namespace; CalDAV/CalendarServer via C:/CS: prefix.
// ─────────────────────────────────────────────────────────────────────────────

const xmlNS = `xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/" xmlns:IC="http://apple.com/ns/ical/"`

func writeXML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprint(w, body)
}

func xmlMS(responses string) string {
	return `<multistatus ` + xmlNS + `>` + responses + `</multistatus>`
}

func xmlMSSync(responses, token string) string {
	return `<multistatus ` + xmlNS + `>` + responses +
		`<sync-token>` + xmlEsc(token) + `</sync-token>` +
		`</multistatus>`
}

func xmlResp(href string, propstats ...string) string {
	return `<response><href>` + xmlEsc(href) + `</href>` +
		strings.Join(propstats, "") + `</response>`
}

func xmlPS(status int, props ...string) string {
	statusLine := "HTTP/1.1 " + strconv.Itoa(status) + " " + http.StatusText(status)
	return `<propstat><prop>` +
		strings.Join(props, "") +
		`</prop><status>` + statusLine + `</status></propstat>`
}

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

func validID(id string) bool { return len(id) > 0 && validIDRe.MatchString(id) }

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

func noteETag(content string) string {
	h := md5.Sum([]byte(content))
	return hex.EncodeToString(h[:8])
}

func syncToken(notes []noteMeta) string {
	sorted := make([]noteMeta, len(notes))
	copy(sorted, notes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	h := md5.New()
	for _, n := range sorted {
		fmt.Fprintf(h, "%s:%d|", n.ID, n.UpdatedAt)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func parseMultigetHrefs(body, calPathPrefix string) map[string]bool {
	result := map[string]bool{}
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
