package caldav

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mock implementations
// ─────────────────────────────────────────────────────────────────────────────

type mockAuthAgent struct {
	tokens map[string]string // token → username
}

func (m *mockAuthAgent) ValidateAutoLoginToken(token string) (bool, string) {
	if owner, ok := m.tokens[token]; ok {
		return true, owner
	}
	return false, ""
}

type mockNote struct {
	content string
	ts      int64
}

type mockUser struct {
	username string
	notes    map[string]*mockNote
}

func (m *mockUser) GetUsername() string { return m.username }

func (m *mockUser) LoadNotes() ([]noteMeta, error) {
	var result []noteMeta
	for id, n := range m.notes {
		result = append(result, noteMeta{
			ID:        id,
			Title:     extractTitle(n.content),
			UpdatedAt: n.ts,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (m *mockUser) ReadNoteContent(id string) (string, int64) {
	if n, ok := m.notes[id]; ok {
		return n.content, n.ts
	}
	return "", 0
}

func (m *mockUser) WriteNote(id, content string, ts int64) error {
	if m.notes == nil {
		m.notes = make(map[string]*mockNote)
	}
	m.notes[id] = &mockNote{content: content, ts: ts}
	return nil
}

func (m *mockUser) DeleteNote(id string) error {
	delete(m.notes, id)
	return nil
}

type mockBackend struct {
	auth  *mockAuthAgent
	users map[string]*mockUser
}

func (m *mockBackend) GetAuthAgent() caldavAuthAgent { return m.auth }

func (m *mockBackend) GetUser(username string) (caldavUser, error) {
	if u, ok := m.users[username]; ok {
		return u, nil
	}
	return nil, fmt.Errorf("user not found: %s", username)
}

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

const (
	testUser  = "alice"
	testToken = "test-token-abc"
)

func newTestManager() *Manager {
	backend := &mockBackend{
		auth: &mockAuthAgent{tokens: map[string]string{testToken: testUser}},
		users: map[string]*mockUser{
			testUser: {
				username: testUser,
				notes: map[string]*mockNote{
					"note1": {content: "Buy milk\nRemember to buy 2% milk.", ts: 1700000000000},
					"note2": {content: "Call dentist", ts: 1700000001000},
				},
			},
		},
	}
	return newManagerWithBackend(backend, true)
}

func do(mgr *Manager, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.SetBasicAuth(testUser, testToken)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	mgr.HandleRequest(rr, req)
	return rr
}

func assertStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Errorf("status %d, want %d\nbody: %s", rr.Code, want, rr.Body.String())
	}
}

func assertContains(t *testing.T, rr *httptest.ResponseRecorder, substr string) {
	t.Helper()
	if !strings.Contains(rr.Body.String(), substr) {
		t.Errorf("body does not contain %q\nbody: %s", substr, rr.Body.String())
	}
}

func assertNotContains(t *testing.T, rr *httptest.ResponseRecorder, substr string) {
	t.Helper()
	if strings.Contains(rr.Body.String(), substr) {
		t.Errorf("body should NOT contain %q\nbody: %s", substr, rr.Body.String())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

func TestOptions(t *testing.T) {
	mgr := newTestManager()
	req := httptest.NewRequest(http.MethodOptions, "/caldav/", nil)
	rr := httptest.NewRecorder()
	mgr.HandleRequest(rr, req)
	assertStatus(t, rr, http.StatusOK)
	if !strings.Contains(rr.Header().Get("DAV"), "calendar-access") {
		t.Errorf("DAV header missing calendar-access: %q", rr.Header().Get("DAV"))
	}
	if !strings.Contains(rr.Header().Get("Allow"), "PROPFIND") {
		t.Errorf("Allow header missing PROPFIND: %q", rr.Header().Get("Allow"))
	}
}

func TestDisabledService(t *testing.T) {
	mgr := newManagerWithBackend(&mockBackend{
		auth:  &mockAuthAgent{tokens: map[string]string{testToken: testUser}},
		users: map[string]*mockUser{testUser: {username: testUser}},
	}, false)
	rr := do(mgr, "PROPFIND", "/caldav/", "", nil)
	assertStatus(t, rr, http.StatusServiceUnavailable)
}

func TestRootDiscoveryNoAuth(t *testing.T) {
	// Root PROPFIND must succeed WITHOUT credentials — Apple accountsd probes first with no auth.
	mgr := newTestManager()
	req := httptest.NewRequest("PROPFIND", "/caldav/", nil) // deliberately no auth
	rr := httptest.NewRecorder()
	mgr.HandleRequest(rr, req)
	assertStatus(t, rr, http.StatusMultiStatus)
	assertContains(t, rr, "current-user-principal")
	assertContains(t, rr, "/caldav/current-user-principal/")
}

func TestAuthRequired(t *testing.T) {
	// Auth is required on the current-user-principal URL (not the root).
	mgr := newTestManager()
	req := httptest.NewRequest("PROPFIND", "/caldav/current-user-principal/", nil) // no auth
	rr := httptest.NewRecorder()
	mgr.HandleRequest(rr, req)
	assertStatus(t, rr, http.StatusUnauthorized)
	if !strings.Contains(rr.Header().Get("WWW-Authenticate"), "Basic") {
		t.Errorf("WWW-Authenticate missing Basic: %q", rr.Header().Get("WWW-Authenticate"))
	}
}

func TestAuthWrongToken(t *testing.T) {
	mgr := newTestManager()
	req := httptest.NewRequest("PROPFIND", "/caldav/current-user-principal/", nil)
	req.SetBasicAuth(testUser, "wrong-token")
	rr := httptest.NewRecorder()
	mgr.HandleRequest(rr, req)
	assertStatus(t, rr, http.StatusUnauthorized)
}

// ── iOS discovery step 1: unauthenticated root probe ────────────────────────

func TestDiscoveryReturnsPrincipal(t *testing.T) {
	// Root PROPFIND is unauthenticated. It returns a FIXED current-user-principal URL.
	mgr := newTestManager()
	req := httptest.NewRequest("PROPFIND", "/caldav/", nil) // no auth — intentional
	rr := httptest.NewRecorder()
	mgr.HandleRequest(rr, req)
	assertStatus(t, rr, http.StatusMultiStatus)
	assertContains(t, rr, "current-user-principal")
	assertContains(t, rr, "/caldav/current-user-principal/")
	// Root must NOT return a real (200) calendar-home-set href; it only appears in
	// the 404 propstat to satisfy RFC 4918 §9.1. A real href would mean step 2 is
	// bypassed and the client tries to skip authentication.
	body := rr.Body.String()
	if strings.Contains(body, `<C:calendar-home-set><href>`) {
		t.Errorf("root must not contain a real calendar-home-set href (only 404 propstat allowed)\nbody: %s", body)
	}
}

func TestDiscoveryXMLStructure(t *testing.T) {
	mgr := newTestManager()
	req := httptest.NewRequest("PROPFIND", "/caldav/", nil) // no auth — root is public
	rr := httptest.NewRecorder()
	mgr.HandleRequest(rr, req)
	body := rr.Body.String()
	if !strings.HasPrefix(body, `<?xml`) {
		t.Errorf("response should start with XML declaration, got: %s", body[:min(50, len(body))])
	}
	assertContains(t, rr, `xmlns="DAV:"`)
	assertContains(t, rr, `<multistatus`)
	assertContains(t, rr, `<response>`)
	assertContains(t, rr, `<href>/caldav/</href>`)
	assertContains(t, rr, `<status>HTTP/1.1 200 OK</status>`)
}

// ── iOS discovery step 2: authenticated current-user-principal ───────────────

func TestCurrentUserPrincipalReturnsCalendarHome(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/current-user-principal/", "", nil)
	assertStatus(t, rr, http.StatusMultiStatus)
	assertContains(t, rr, "C:calendar-home-set")
	assertContains(t, rr, "/caldav/"+testUser+"/")
}

func TestCurrentUserPrincipalHomeSetDiffersFromPrincipal(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/current-user-principal/", "", nil)
	body := rr.Body.String()
	principalURL := "/caldav/current-user-principal/"
	homeURL := "/caldav/" + testUser + "/"
	if !strings.Contains(body, homeURL) {
		t.Errorf("calendar-home-set not pointing to %s\nbody: %s", homeURL, body)
	}
	if principalURL == homeURL {
		t.Error("principal URL and calendar home URL must differ for iOS to accept the account")
	}
}

func TestCurrentUserPrincipalXMLProperties(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/current-user-principal/", "", nil)
	assertContains(t, rr, "C:calendar-home-set")
	assertContains(t, rr, "C:calendar-user-address-set")
	assertContains(t, rr, "<principal/>")
}

// ── Legacy /caldav/principals/{user}/ URL (kept for backwards compat) ─────────

func TestPrincipalReturnsCalendarHomeSet(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/principals/"+testUser+"/", "", nil)
	assertStatus(t, rr, http.StatusMultiStatus)
	assertContains(t, rr, "C:calendar-home-set")
	assertContains(t, rr, "/caldav/"+testUser+"/")
}

func TestPrincipalHomeSetDiffersFromPrincipal(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/principals/"+testUser+"/", "", nil)
	body := rr.Body.String()
	principalURL := "/caldav/principals/" + testUser + "/"
	homeURL := "/caldav/" + testUser + "/"
	if !strings.Contains(body, homeURL) {
		t.Errorf("calendar-home-set not pointing to %s\nbody: %s", homeURL, body)
	}
	if principalURL == homeURL {
		t.Error("principal URL and calendar home URL must differ for iOS to accept the account")
	}
}

func TestPrincipalXMLProperties(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/principals/"+testUser+"/", "", nil)
	assertContains(t, rr, "C:calendar-home-set")
	assertContains(t, rr, "C:calendar-user-address-set")
	assertContains(t, rr, "<principal/>")
}

// ── iOS discovery step 3: calendar home ─────────────────────────────────────

func TestCalendarHomeDepth0(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/"+testUser+"/", "", map[string]string{"Depth": "0"})
	assertStatus(t, rr, http.StatusMultiStatus)
	// Depth:0 returns home-level discovery properties including calendar-home-set
	assertContains(t, rr, "C:calendar-home-set")
	assertContains(t, rr, "current-user-principal")
	// but should NOT return child calendar resources (no /notes/ listing)
	assertNotContains(t, rr, "<C:calendar/>")
	assertNotContains(t, rr, "/notes/")
}

func TestCalendarHomeDepth1ListsNotes(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/"+testUser+"/", "", map[string]string{"Depth": "1"})
	assertStatus(t, rr, http.StatusMultiStatus)
	assertContains(t, rr, "/caldav/"+testUser+"/notes/")
	assertContains(t, rr, "C:calendar")
	assertContains(t, rr, "C:supported-calendar-component-set")
	assertContains(t, rr, `name="VTODO"`)
}

func TestCalendarHomeNoDepthHeaderListsNotes(t *testing.T) {
	// RFC 4918: when Depth header is absent the default is infinity.
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/"+testUser+"/", "", nil)
	assertStatus(t, rr, http.StatusMultiStatus)
	assertContains(t, rr, "/caldav/"+testUser+"/notes/")
	assertContains(t, rr, `name="VTODO"`)
}

// ── Notes calendar collection ────────────────────────────────────────────────

func TestCalendarPropfind(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/"+testUser+"/notes/", "", nil)
	assertStatus(t, rr, http.StatusMultiStatus)
	assertContains(t, rr, "C:calendar")
	assertContains(t, rr, "Notes")
	assertContains(t, rr, "CS:getctag")
	assertContains(t, rr, `name="VTODO"`)
}

func TestCalendarPropfindDepth1IncludesItems(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/"+testUser+"/notes/", "", map[string]string{"Depth": "1"})
	assertStatus(t, rr, http.StatusMultiStatus)
	assertContains(t, rr, "note1.ics")
	assertContains(t, rr, "note2.ics")
	assertContains(t, rr, "C:calendar-data")
	assertContains(t, rr, "BEGIN:VTODO")
}

func TestCalendarReport(t *testing.T) {
	mgr := newTestManager()
	body := `<?xml version="1.0"?><C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`
	rr := do(mgr, "REPORT", "/caldav/"+testUser+"/notes/", body, nil)
	assertStatus(t, rr, http.StatusMultiStatus)
	assertContains(t, rr, "BEGIN:VTODO")
	assertContains(t, rr, "note1.ics")
	assertContains(t, rr, "note2.ics")
}

func TestCalendarSyncReport(t *testing.T) {
	mgr := newTestManager()
	body := `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:sync-token/><D:prop><D:getetag/></D:prop></D:sync-collection>`
	rr := do(mgr, "REPORT", "/caldav/"+testUser+"/notes/", body, nil)
	assertStatus(t, rr, http.StatusMultiStatus)
	// sync-collection response: first entry is the collection itself
	assertContains(t, rr, "/caldav/"+testUser+"/notes/")
	assertContains(t, rr, "httpd/unix-directory")
	// item entries have etag and content-type but NOT inline calendar-data
	assertContains(t, rr, "note1.ics")
	assertContains(t, rr, "text/calendar")
	assertNotContains(t, rr, "BEGIN:VTODO") // full data comes in a multiget, not sync
	// sync-token at end of multistatus
	assertContains(t, rr, "<sync-token>")
}

func TestCalendarSyncReportFollowedByMultiget(t *testing.T) {
	// Simulate the two-step sync: sync-collection → list etags, then multiget → full data.
	mgr := newTestManager()

	syncBody := `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:sync-token/><D:prop><D:getetag/></D:prop></D:sync-collection>`
	rr := do(mgr, "REPORT", "/caldav/"+testUser+"/notes/", syncBody, nil)
	assertStatus(t, rr, http.StatusMultiStatus)
	assertContains(t, rr, "note1.ics")
	assertNotContains(t, rr, "BEGIN:VTODO")

	// Now fetch the full data via multiget.
	multigetBody := `<?xml version="1.0"?><C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:prop><D:getetag/><C:calendar-data/></D:prop>` +
		`<D:href>/caldav/` + testUser + `/notes/note1.ics</D:href>` +
		`</C:calendar-multiget>`
	rr2 := do(mgr, "REPORT", "/caldav/"+testUser+"/notes/", multigetBody, nil)
	assertStatus(t, rr2, http.StatusMultiStatus)
	assertContains(t, rr2, "BEGIN:VTODO")
	assertContains(t, rr2, "CALSCALE:GREGORIAN")
	assertContains(t, rr2, "UID:note1@arozos")
}

// ── Individual note items ─────────────────────────────────────────────────────

func TestGetNote(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "GET", "/caldav/"+testUser+"/notes/note1.ics", "", nil)
	assertStatus(t, rr, http.StatusOK)
	assertContains(t, rr, "BEGIN:VCALENDAR")
	assertContains(t, rr, "BEGIN:VTODO")
	assertContains(t, rr, "UID:note1@arozos")
	assertContains(t, rr, "SUMMARY:Buy milk")
	assertContains(t, rr, "END:VTODO")
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/calendar") {
		t.Errorf("Content-Type = %q, want text/calendar", ct)
	}
	if rr.Header().Get("ETag") == "" {
		t.Error("ETag header missing")
	}
}

func TestGetNoteNotFound(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "GET", "/caldav/"+testUser+"/notes/no-such-note.ics", "", nil)
	assertStatus(t, rr, http.StatusNotFound)
}

func TestPutNote(t *testing.T) {
	mgr := newTestManager()
	ical := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VTODO\r\n" +
		"UID:note3@arozos\r\n" +
		"SUMMARY:New task\r\n" +
		"DESCRIPTION:Details here\r\n" +
		"END:VTODO\r\nEND:VCALENDAR\r\n"
	rr := do(mgr, "PUT", "/caldav/"+testUser+"/notes/note3.ics", ical, nil)
	assertStatus(t, rr, http.StatusCreated)
	if rr.Header().Get("ETag") == "" {
		t.Error("ETag missing from PUT response")
	}

	// Verify the note was created
	rr2 := do(mgr, "GET", "/caldav/"+testUser+"/notes/note3.ics", "", nil)
	assertStatus(t, rr2, http.StatusOK)
	assertContains(t, rr2, "Details here")
}

func TestDeleteNote(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "DELETE", "/caldav/"+testUser+"/notes/note1.ics", "", nil)
	assertStatus(t, rr, http.StatusNoContent)

	// Verify deleted
	rr2 := do(mgr, "GET", "/caldav/"+testUser+"/notes/note1.ics", "", nil)
	assertStatus(t, rr2, http.StatusNotFound)
}

func TestPropfindItem(t *testing.T) {
	mgr := newTestManager()
	rr := do(mgr, "PROPFIND", "/caldav/"+testUser+"/notes/note1.ics", "", nil)
	assertStatus(t, rr, http.StatusMultiStatus)
	assertContains(t, rr, "note1.ics")
	assertContains(t, rr, "getetag")
}

// ── ctag stability ───────────────────────────────────────────────────────────

func TestCtagIsStable(t *testing.T) {
	mgr := newTestManager()
	rr1 := do(mgr, "PROPFIND", "/caldav/"+testUser+"/notes/", "", nil)
	rr2 := do(mgr, "PROPFIND", "/caldav/"+testUser+"/notes/", "", nil)
	// Extract ctag values from both responses
	getCtag := func(body string) string {
		start := strings.Index(body, "<CS:getctag>")
		end := strings.Index(body, "</CS:getctag>")
		if start < 0 || end < 0 {
			return ""
		}
		return body[start+len("<CS:getctag>") : end]
	}
	c1 := getCtag(rr1.Body.String())
	c2 := getCtag(rr2.Body.String())
	if c1 == "" {
		t.Error("ctag not found in response")
	}
	if c1 != c2 {
		t.Errorf("ctag changed between requests without any note changes: %q vs %q", c1, c2)
	}
}

func TestCtagChangesAfterWrite(t *testing.T) {
	mgr := newTestManager()
	user := mgr.backend.(*mockBackend).users[testUser]

	rr1 := do(mgr, "PROPFIND", "/caldav/"+testUser+"/notes/", "", nil)
	getCtag := func(body string) string {
		start := strings.Index(body, "<CS:getctag>")
		end := strings.Index(body, "</CS:getctag>")
		if start < 0 || end < 0 {
			return ""
		}
		return body[start+len("<CS:getctag>") : end]
	}
	c1 := getCtag(rr1.Body.String())

	// Mutate a note directly via the mock
	user.notes["note1"].ts += 1000

	rr2 := do(mgr, "PROPFIND", "/caldav/"+testUser+"/notes/", "", nil)
	c2 := getCtag(rr2.Body.String())
	if c1 == c2 {
		t.Error("ctag did not change after note was modified")
	}
}

// ── Full iOS account-setup simulation ────────────────────────────────────────
//
// Apple accountsd discovery sequence:
//  1. OPTIONS  /caldav/                       (no auth) → 200
//  2. PROPFIND /caldav/                       (no auth) → 207  current-user-principal = /caldav/current-user-principal/
//  3. PROPFIND /caldav/current-user-principal/ (no auth) → 401  WWW-Authenticate
//  4. PROPFIND /caldav/current-user-principal/ (with auth) → 207  calendar-home-set = /caldav/{user}/
//  5. PROPFIND /caldav/{user}/                Depth:1  → 207  Notes calendar listed

func TestIOSDiscoveryFlow(t *testing.T) {
	mgr := newTestManager()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/caldav") {
			mgr.HandleRequest(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// accountsd does NOT follow redirects between these probes — use a client
	// that stops at the first 401 so we can inspect the challenge header.
	noRedirectClient := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	t.Run("Step0_OPTIONS_no_auth", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/caldav/", nil)
		resp, err := noRedirectClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("OPTIONS status %d, want 200", resp.StatusCode)
		}
		if !strings.Contains(resp.Header.Get("DAV"), "calendar-access") {
			t.Errorf("DAV header missing calendar-access: %q", resp.Header.Get("DAV"))
		}
	})

	var principalURL string
	t.Run("Step1_Root_probe_no_auth", func(t *testing.T) {
		// accountsd sends this with NO credentials. We MUST return 207, not 401.
		req, _ := http.NewRequest("PROPFIND", srv.URL+"/caldav/", nil) // no auth
		req.Header.Set("Depth", "0")
		resp, err := noRedirectClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMultiStatus {
			t.Fatalf("step 1: status %d, want 207 — returning 401 here causes accountsd to give up", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)
		if !strings.Contains(bodyStr, "current-user-principal") {
			t.Fatal("step 1: current-user-principal missing from root response")
		}
		principalURL = "/caldav/current-user-principal/"
		if !strings.Contains(bodyStr, principalURL) {
			t.Errorf("step 1: expected principal URL %q not found\nbody: %s", principalURL, bodyStr)
		}
		t.Logf("Step 1 body:\n%s", bodyStr)
	})

	t.Run("Step2a_Principal_no_auth_gets_401", func(t *testing.T) {
		// accountsd follows principalURL without auth first — must get 401 challenge.
		req, _ := http.NewRequest("PROPFIND", srv.URL+principalURL, nil) // no auth
		req.Header.Set("Depth", "0")
		resp, err := noRedirectClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("step 2a: status %d, want 401 (challenge must be here, not at root)", resp.StatusCode)
		}
		if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Basic") {
			t.Errorf("step 2a: WWW-Authenticate missing Basic: %q", resp.Header.Get("WWW-Authenticate"))
		}
	})

	t.Run("Step2b_Principal_with_auth_returns_home", func(t *testing.T) {
		// accountsd retries with credentials after the 401 challenge.
		req, _ := http.NewRequest("PROPFIND", srv.URL+principalURL, nil)
		req.SetBasicAuth(testUser, testToken)
		req.Header.Set("Depth", "0")
		resp, err := noRedirectClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMultiStatus {
			t.Errorf("step 2b: status %d, want 207", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)
		if !strings.Contains(bodyStr, "C:calendar-home-set") {
			t.Error("step 2b: calendar-home-set missing from principal response")
		}
		wantHome := "/caldav/" + testUser + "/"
		if !strings.Contains(bodyStr, wantHome) {
			t.Errorf("step 2b: calendar home URL %q missing\nbody: %s", wantHome, bodyStr)
		}
		t.Logf("Step 2b body:\n%s", bodyStr)
	})

	t.Run("Step3_List_Calendars", func(t *testing.T) {
		req, _ := http.NewRequest("PROPFIND", srv.URL+"/caldav/"+testUser+"/", nil)
		req.SetBasicAuth(testUser, testToken)
		req.Header.Set("Depth", "1")
		resp, err := noRedirectClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMultiStatus {
			t.Errorf("step 3 status %d, want 207", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)
		if !strings.Contains(bodyStr, `name="VTODO"`) {
			t.Error("step 3: no VTODO calendar found — iOS Reminders will not connect")
		}
		if !strings.Contains(bodyStr, "/caldav/"+testUser+"/notes/") {
			t.Error("step 3: Notes calendar URL missing")
		}
		t.Logf("Step 3 body:\n%s", bodyStr)
	})

	t.Log("iOS discovery flow: all steps passed — account confirmation should succeed")
}

// ─────────────────────────────────────────────────────────────────────────────
// iCalendar helpers
// ─────────────────────────────────────────────────────────────────────────────

func TestNoteToIcal(t *testing.T) {
	ical := noteToIcal("abc123", "Buy groceries\nMilk, eggs, bread", 1700000000000)
	checks := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"BEGIN:VTODO",
		"UID:abc123@arozos",
		"SUMMARY:Buy groceries",
		"DESCRIPTION:Buy groceries",
		"STATUS:NEEDS-ACTION",
		"END:VTODO",
		"END:VCALENDAR",
	}
	for _, c := range checks {
		if !strings.Contains(ical, c) {
			t.Errorf("iCal missing %q\nfull output:\n%s", c, ical)
		}
	}
}

func TestParseVTODO(t *testing.T) {
	ical := "BEGIN:VCALENDAR\r\nBEGIN:VTODO\r\n" +
		"UID:note5@arozos\r\n" +
		"SUMMARY:Hello world\r\n" +
		"DESCRIPTION:Line one\\nLine two\r\n" +
		"END:VTODO\r\nEND:VCALENDAR\r\n"
	summary, description, uid := parseVTODO(ical)
	if summary != "Hello world" {
		t.Errorf("summary = %q", summary)
	}
	if !strings.Contains(description, "Line one") {
		t.Errorf("description = %q", description)
	}
	if uid != "note5" {
		t.Errorf("uid = %q", uid)
	}
}

func TestIcalLineFolding(t *testing.T) {
	long := strings.Repeat("x", 200)
	ical := noteToIcal("id1", long, 0)
	for _, line := range strings.Split(ical, "\r\n") {
		if len(line) > 75 {
			t.Errorf("iCal line too long (%d chars): %q", len(line), line)
		}
	}
}

func TestSyncTokenStable(t *testing.T) {
	notes := []noteMeta{
		{ID: "a", UpdatedAt: 100},
		{ID: "b", UpdatedAt: 200},
	}
	t1 := syncToken(notes)
	t2 := syncToken(notes)
	if t1 != t2 {
		t.Errorf("syncToken not stable: %q vs %q", t1, t2)
	}
}

func TestSyncTokenOrderIndependent(t *testing.T) {
	n1 := []noteMeta{{ID: "a", UpdatedAt: 100}, {ID: "b", UpdatedAt: 200}}
	n2 := []noteMeta{{ID: "b", UpdatedAt: 200}, {ID: "a", UpdatedAt: 100}}
	if syncToken(n1) != syncToken(n2) {
		t.Error("syncToken depends on note order")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
