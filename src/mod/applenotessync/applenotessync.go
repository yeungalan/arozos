package applenotessync

/*
	Apple Notes Sync Module
	Syncs notes between arozos Notes app and Apple Notes via IMAP.

	Apple Notes stores notes as emails in an IMAP folder named "Notes"
	on imap.mail.me.com (iCloud). Each note is an RFC 2822 message with
	Content-Type: text/html and X-Uniform-Type-Identifier: com.apple.mail-note.
*/

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	imapClient "github.com/emersion/go-imap/client"

	db "imuslab.com/arozos/mod/database"
	"imuslab.com/arozos/mod/info/logger"
	user "imuslab.com/arozos/mod/user"
	"imuslab.com/arozos/mod/utils"
)

const dbTable = "apple_notes_sync"

// Options holds dependencies for the sync handler.
type Options struct {
	UserHandler *user.UserHandler
	Database    *db.Database
	Logger      *logger.Logger
}

// Handler provides HTTP handlers and sync logic.
type Handler struct {
	opts Options
}

// SyncConfig stores the user's IMAP credentials.
type SyncConfig struct {
	IMAPServer string `json:"imapServer"`
	IMAPPort   int    `json:"imapPort"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Enabled    bool   `json:"enabled"`
}

// SyncEntry maps one arozos note to one Apple Note by IMAP UID.
type SyncEntry struct {
	ArozosID string `json:"arozosId"`
	AppleUID uint32 `json:"appleUid"`
}

// SyncStatus records the outcome of the most recent sync.
type SyncStatus struct {
	LastSyncTime int64  `json:"lastSyncTime"`
	LastError    string `json:"lastError"`
	Pulled       int    `json:"pulled"`
	Pushed       int    `json:"pushed"`
	InProgress   bool   `json:"inProgress"`
}

// notesMeta mirrors the meta.json written by the AGI scripts.
type notesMeta struct {
	LastOpened string     `json:"lastOpened"`
	Theme      string     `json:"theme"`
	Notes      []noteMeta `json:"notes"`
}

type noteMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
}

// NewHandler creates a sync handler and ensures the DB table exists.
func NewHandler(opts Options) *Handler {
	opts.Database.NewTable(dbTable)
	return &Handler{opts: opts}
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

// HandleConfig responds to GET/POST/DELETE on the sync configuration endpoint.
func (h *Handler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	userinfo, err := h.opts.UserHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "authentication required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		cfg := h.loadConfig(userinfo.Username)
		resp := map[string]interface{}{
			"imapServer":  cfg.IMAPServer,
			"imapPort":    cfg.IMAPPort,
			"username":    cfg.Username,
			"hasPassword": cfg.Password != "",
			"enabled":     cfg.Enabled,
		}
		js, _ := json.Marshal(resp)
		utils.SendJSONResponse(w, string(js))

	case http.MethodPost:
		var incoming SyncConfig
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			utils.SendErrorResponse(w, "invalid JSON body")
			return
		}
		// Preserve stored password when the client omits it
		if incoming.Password == "" {
			existing := h.loadConfig(userinfo.Username)
			incoming.Password = existing.Password
		}
		if incoming.IMAPPort == 0 {
			incoming.IMAPPort = 993
		}
		if incoming.IMAPServer == "" {
			incoming.IMAPServer = "imap.mail.me.com"
		}
		if err := h.opts.Database.Write(dbTable, userinfo.Username+":config", incoming); err != nil {
			utils.SendErrorResponse(w, "failed to save configuration")
			return
		}
		utils.SendOK(w)

	case http.MethodDelete:
		h.opts.Database.Delete(dbTable, userinfo.Username+":config")
		h.opts.Database.Delete(dbTable, userinfo.Username+":map")
		utils.SendOK(w)

	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// HandleSync triggers an asynchronous sync and returns immediately.
func (h *Handler) HandleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	userinfo, err := h.opts.UserHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "authentication required")
		return
	}

	cfg := h.loadConfig(userinfo.Username)
	if !cfg.Enabled || cfg.Username == "" || cfg.Password == "" {
		utils.SendErrorResponse(w, "sync not configured or disabled")
		return
	}

	status := h.loadStatus(userinfo.Username)
	if status.InProgress {
		utils.SendErrorResponse(w, "sync already in progress")
		return
	}

	go h.runSync(userinfo.Username, cfg)
	utils.SendOK(w)
}

// HandleStatus returns the latest sync status for the authenticated user.
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	userinfo, err := h.opts.UserHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "authentication required")
		return
	}
	status := h.loadStatus(userinfo.Username)
	js, _ := json.Marshal(status)
	utils.SendJSONResponse(w, string(js))
}

// ── Sync orchestration ────────────────────────────────────────────────────────

func (h *Handler) runSync(username string, cfg SyncConfig) {
	status := h.loadStatus(username)
	status.InProgress = true
	h.saveStatus(username, status)

	pulled, pushed, syncErr := h.syncNotes(username, cfg)

	status.InProgress = false
	status.LastSyncTime = time.Now().UnixMilli()
	status.Pulled = pulled
	status.Pushed = pushed
	if syncErr != nil {
		status.LastError = syncErr.Error()
		h.opts.Logger.PrintAndLog("AppleNotesSync", "sync error for "+username+": "+syncErr.Error(), syncErr)
	} else {
		status.LastError = ""
	}
	h.saveStatus(username, status)
}

func (h *Handler) syncNotes(username string, cfg SyncConfig) (pulled, pushed int, retErr error) {
	c, err := dialIMAP(cfg)
	if err != nil {
		return 0, 0, fmt.Errorf("IMAP connect: %w", err)
	}
	defer c.Logout()

	mbox, err := c.Select("Notes", false)
	if err != nil {
		return 0, 0, fmt.Errorf("select Notes mailbox: %w", err)
	}

	notesDir, err := h.resolveNotesDir(username)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve notes directory: %w", err)
	}

	meta := loadNotesMeta(notesDir)
	entries := h.loadSyncEntries(username)
	lastSync := time.UnixMilli(h.loadStatus(username).LastSyncTime)

	// Build in-memory lookup tables
	arozosToUID := map[string]uint32{}
	uidToArozos := map[uint32]string{}
	for _, e := range entries {
		arozosToUID[e.ArozosID] = e.AppleUID
		uidToArozos[e.AppleUID] = e.ArozosID
	}

	// ── Pull: Apple → arozos ─────────────────────────────────────────────
	appleUIDs := []uint32{}
	if mbox.Messages > 0 {
		criteria := imap.NewSearchCriteria()
		appleUIDs, err = c.UidSearch(criteria)
		if err != nil {
			return 0, 0, fmt.Errorf("IMAP search: %w", err)
		}
	}

	// Track which Apple UIDs still exist (for future deletion sync)
	existingAppleUIDs := map[uint32]bool{}
	for _, uid := range appleUIDs {
		existingAppleUIDs[uid] = true
	}

	if len(appleUIDs) > 0 {
		seqset := new(imap.SeqSet)
		seqset.AddNum(appleUIDs...)

		section := &imap.BodySectionName{}
		fetchItems := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, section.FetchItem()}
		msgCh := make(chan *imap.Message, 20)
		doneCh := make(chan error, 1)
		go func() { doneCh <- c.UidFetch(seqset, fetchItems, msgCh) }()

		for msg := range msgCh {
			uid := msg.Uid
			if uid == 0 || msg.Envelope == nil {
				continue
			}

			subject := msg.Envelope.Subject
			noteDate := msg.Envelope.Date
			needsPull := noteDate.After(lastSync) || lastSync.IsZero()

			existingID, alreadySynced := uidToArozos[uid]

			if alreadySynced && !needsPull {
				continue // unchanged since last sync
			}

			bodyR := msg.GetBody(section)
			if bodyR == nil {
				continue
			}
			content, parseErr := extractTextFromMessage(bodyR)
			if parseErr != nil {
				continue
			}

			title := derivedTitle(subject, content)
			ts := noteDate.UnixMilli()
			if ts <= 0 {
				ts = time.Now().UnixMilli()
			}

			if alreadySynced {
				// Overwrite arozos note with the Apple version
				if os.WriteFile(filepath.Join(notesDir, existingID+".txt"), []byte(content), 0644) == nil {
					updateMetaEntry(meta, existingID, title, ts)
					pulled++
				}
			} else {
				// Create a new arozos note for this Apple note
				newID := "apple_" + strconv.FormatUint(uint64(uid), 16)
				if os.WriteFile(filepath.Join(notesDir, newID+".txt"), []byte(content), 0644) == nil {
					meta.Notes = append(meta.Notes, noteMeta{ID: newID, Title: title, UpdatedAt: ts})
					entries = append(entries, SyncEntry{ArozosID: newID, AppleUID: uid})
					arozosToUID[newID] = uid
					uidToArozos[uid] = newID
					pulled++
				}
			}
		}
		if fetchErr := <-doneCh; fetchErr != nil {
			h.opts.Logger.PrintAndLog("AppleNotesSync", "fetch partial error: "+fetchErr.Error(), fetchErr)
		}
	}

	// ── Push: arozos → Apple ─────────────────────────────────────────────
	for _, nm := range meta.Notes {
		if _, ok := arozosToUID[nm.ID]; ok {
			continue // already tracked
		}
		rawContent, err := os.ReadFile(filepath.Join(notesDir, nm.ID+".txt"))
		if err != nil {
			continue
		}
		title := nm.Title
		if title == "" {
			title = derivedTitle("", string(rawContent))
		}
		appleHTML := wrapInAppleHTML(string(rawContent))
		rawMsg := buildRawMessage(title, appleHTML)

		if appendErr := c.Append("Notes", []string{imap.SeenFlag}, time.Now(), strings.NewReader(rawMsg)); appendErr != nil {
			continue
		}
		pushed++

		// Retrieve the UID of the note we just created by searching on Subject
		sc := imap.NewSearchCriteria()
		sc.Header = make(textproto.MIMEHeader)
		sc.Header["Subject"] = []string{title}
		newUIDs, searchErr := c.UidSearch(sc)
		if searchErr == nil && len(newUIDs) > 0 {
			newUID := newUIDs[len(newUIDs)-1]
			entries = append(entries, SyncEntry{ArozosID: nm.ID, AppleUID: newUID})
			arozosToUID[nm.ID] = newUID
			uidToArozos[newUID] = nm.ID
		}
	}

	// Persist updated state
	saveNotesMeta(notesDir, meta)
	h.saveSyncEntries(username, entries)

	return pulled, pushed, nil
}

// ── IMAP helpers ──────────────────────────────────────────────────────────────

func dialIMAP(cfg SyncConfig) (*imapClient.Client, error) {
	addr := fmt.Sprintf("%s:%d", cfg.IMAPServer, cfg.IMAPPort)
	c, err := imapClient.DialTLS(addr, &tls.Config{ServerName: cfg.IMAPServer})
	if err != nil {
		return nil, err
	}
	if err := c.Login(cfg.Username, cfg.Password); err != nil {
		c.Logout()
		return nil, fmt.Errorf("login failed: %w", err)
	}
	return c, nil
}

// extractTextFromMessage parses a raw RFC 2822 message and returns plain text.
func extractTextFromMessage(r io.Reader) (string, error) {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return "", err
	}
	ct := msg.Header.Get("Content-Type")
	cte := strings.ToLower(msg.Header.Get("Content-Transfer-Encoding"))
	mediaType, params, _ := mime.ParseMediaType(ct)

	decode := func(body io.Reader) io.Reader {
		switch cte {
		case "quoted-printable":
			return quotedprintable.NewReader(body)
		default:
			return body
		}
	}

	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		boundary := params["boundary"]
		mr := multipart.NewReader(msg.Body, boundary)
		htmlPart, textPart := "", ""
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				continue
			}
			partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			partCTE := strings.ToLower(part.Header.Get("Content-Transfer-Encoding"))
			var partReader io.Reader = part
			if partCTE == "quoted-printable" {
				partReader = quotedprintable.NewReader(part)
			}
			data, _ := io.ReadAll(partReader)
			switch {
			case strings.EqualFold(partType, "text/html"):
				htmlPart = stripHTML(string(data))
			case strings.EqualFold(partType, "text/plain"):
				textPart = string(data)
			}
		}
		if htmlPart != "" {
			return htmlPart, nil
		}
		return textPart, nil

	case strings.EqualFold(mediaType, "text/html"):
		data, _ := io.ReadAll(decode(msg.Body))
		return stripHTML(string(data)), nil

	default:
		data, _ := io.ReadAll(decode(msg.Body))
		return string(data), nil
	}
}

var (
	tagRE        = regexp.MustCompile(`<[^>]+>`)
	blankLinesRE = regexp.MustCompile(`\n{3,}`)
	blockTagRE   = regexp.MustCompile(`(?i)</?(?:br|p|div|li|h[1-6]|tr)\b[^>]*>`)
)

func stripHTML(h string) string {
	h = blockTagRE.ReplaceAllString(h, "\n")
	h = tagRE.ReplaceAllString(h, "")
	replacer := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&nbsp;", " ", "&quot;", `"`, "&#x27;", "'", "&#39;", "'",
	)
	h = replacer.Replace(h)
	h = blankLinesRE.ReplaceAllString(h, "\n\n")
	return strings.TrimSpace(h)
}

func wrapInAppleHTML(text string) string {
	var sb strings.Builder
	sb.WriteString(`<html><head><meta http-equiv="Content-Type" content="text/html; charset=utf-8" /></head>`)
	sb.WriteString(`<body style="word-wrap: break-word; -webkit-nbsp-mode: space; line-break: after-white-space; ">`)
	for _, line := range strings.Split(text, "\n") {
		esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(line)
		if esc == "" {
			sb.WriteString("<div><br></div>\n")
		} else {
			sb.WriteString("<div>" + esc + "</div>\n")
		}
	}
	sb.WriteString("</body></html>")
	return sb.String()
}

func buildRawMessage(subject, htmlBody string) string {
	now := time.Now().Format(time.RFC1123Z)
	var qp strings.Builder
	w := quotedprintable.NewWriter(&qp)
	w.Write([]byte(htmlBody))
	w.Close()
	return "Date: " + now + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Subject: " + subject + "\r\n" +
		"X-Uniform-Type-Identifier: com.apple.mail-note\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		qp.String()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func derivedTitle(subject, content string) string {
	if subject != "" {
		if len(subject) > 60 {
			return subject[:60]
		}
		return subject
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 60 {
				return line[:60]
			}
			return line
		}
	}
	return "New Note"
}

func updateMetaEntry(meta *notesMeta, id, title string, ts int64) {
	for i, n := range meta.Notes {
		if n.ID == id {
			meta.Notes[i].Title = title
			meta.Notes[i].UpdatedAt = ts
			return
		}
	}
}

// ── Notes filesystem helpers ──────────────────────────────────────────────────

func (h *Handler) resolveNotesDir(username string) (string, error) {
	userinfo, err := h.opts.UserHandler.GetUserInfoFromUsername(username)
	if err != nil {
		return "", err
	}
	homeFSH, err := userinfo.GetHomeFileSystemHandler()
	if err != nil {
		return "", err
	}
	realPath, err := homeFSH.FileSystemAbstraction.VirtualPathToRealPath("user:/Document/Notes", username)
	if err != nil {
		return "", err
	}
	if mkErr := os.MkdirAll(realPath, 0755); mkErr != nil {
		return "", mkErr
	}
	return realPath, nil
}

func loadNotesMeta(dir string) *notesMeta {
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return &notesMeta{Theme: "dark", Notes: []noteMeta{}}
	}
	var m notesMeta
	if json.Unmarshal(data, &m) != nil {
		return &notesMeta{Theme: "dark", Notes: []noteMeta{}}
	}
	if m.Notes == nil {
		m.Notes = []noteMeta{}
	}
	return &m
}

func saveNotesMeta(dir string, m *notesMeta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), data, 0644)
}

// ── Database helpers ──────────────────────────────────────────────────────────

func (h *Handler) loadConfig(username string) SyncConfig {
	var cfg SyncConfig
	h.opts.Database.Read(dbTable, username+":config", &cfg)
	return cfg
}

func (h *Handler) loadStatus(username string) SyncStatus {
	var s SyncStatus
	h.opts.Database.Read(dbTable, username+":status", &s)
	return s
}

func (h *Handler) saveStatus(username string, s SyncStatus) {
	h.opts.Database.Write(dbTable, username+":status", s)
}

func (h *Handler) loadSyncEntries(username string) []SyncEntry {
	var entries []SyncEntry
	h.opts.Database.Read(dbTable, username+":map", &entries)
	if entries == nil {
		entries = []SyncEntry{}
	}
	return entries
}

func (h *Handler) saveSyncEntries(username string, entries []SyncEntry) {
	h.opts.Database.Write(dbTable, username+":map", entries)
}
