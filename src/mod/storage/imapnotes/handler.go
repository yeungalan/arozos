package imapnotes

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	uuid "github.com/satori/go.uuid"
	"imuslab.com/arozos/mod/auth"
	"imuslab.com/arozos/mod/database"
	"imuslab.com/arozos/mod/user"
)

const (
	stateNotAuth  = 0
	stateAuth     = 1
	stateSelected = 2
)

// deletedFlags tracks messages marked \Deleted in the current session.
type connHandler struct {
	conn        net.Conn
	r           *bufio.Reader
	w           *bufio.Writer
	authAgent   *auth.AuthAgent
	userHandler *user.UserHandler
	database    *database.Database
	state       int
	username    string
	// UIDs marked \Deleted this session (expunged on EXPUNGE / CLOSE)
	deleted map[uint32]bool
}

func newConnHandler(conn net.Conn, ag *auth.AuthAgent, uh *user.UserHandler, db *database.Database) *connHandler {
	return &connHandler{
		conn:        conn,
		r:           bufio.NewReader(conn),
		w:           bufio.NewWriter(conn),
		authAgent:   ag,
		userHandler: uh,
		database:    db,
		state:       stateNotAuth,
		deleted:     make(map[uint32]bool),
	}
}

func (h *connHandler) send(s string) {
	h.w.WriteString(s + "\r\n")
	h.w.Flush()
}

func (h *connHandler) sendLiteral(tag, prefix string, data []byte) {
	h.w.WriteString(fmt.Sprintf("%s {%d}\r\n", prefix, len(data)))
	h.w.Flush()
	h.w.Write(data)
	h.w.Flush()
	// tag is written after the literal block inline, but per RFC the server can
	// send the tagged OK on its own line after the untagged literal response.
	// We send the rest of the FETCH line and then the tagged OK separately.
}

func (h *connHandler) run() {
	defer h.conn.Close()
	h.conn.SetDeadline(time.Now().Add(30 * time.Minute))

	h.send("* OK [CAPABILITY IMAP4rev1 AUTH=PLAIN LOGIN] ArozOS Notes IMAP Server ready")

	for {
		line, err := h.readLine()
		if err != nil {
			return
		}
		if line == "" {
			continue
		}

		tag, cmd, args := parseLine(line)
		if tag == "" || cmd == "" {
			continue
		}
		h.conn.SetDeadline(time.Now().Add(30 * time.Minute))

		switch strings.ToUpper(cmd) {
		case "CAPABILITY":
			h.send("* CAPABILITY IMAP4rev1 AUTH=PLAIN LOGIN")
			h.send(tag + " OK CAPABILITY completed")

		case "NOOP":
			h.send(tag + " OK NOOP completed")

		case "LOGOUT":
			h.send("* BYE ArozOS Notes IMAP Server logging out")
			h.send(tag + " OK LOGOUT completed")
			return

		case "LOGIN":
			h.handleLogin(tag, args)

		case "AUTHENTICATE":
			// Only support PLAIN inline; most clients fall back to LOGIN.
			h.send("+ ")
			// Read the base64 credentials line but ignore it since we expect the
			// client to retry with LOGIN if AUTHENTICATE PLAIN fails here.
			h.readLine()
			h.send(tag + " NO AUTHENTICATE failed - please use LOGIN")

		case "LIST":
			h.handleList(tag, args)

		case "LSUB":
			h.handleLsub(tag, args)

		case "SUBSCRIBE", "UNSUBSCRIBE":
			h.send(tag + " OK " + strings.ToUpper(cmd) + " completed")

		case "STATUS":
			h.handleStatus(tag, args)

		case "SELECT":
			h.handleSelect(tag, args, false)

		case "EXAMINE":
			h.handleSelect(tag, args, true)

		case "CLOSE":
			if h.state == stateSelected {
				h.applyExpunge(false)
				h.state = stateAuth
			}
			h.send(tag + " OK CLOSE completed")

		case "APPEND":
			h.handleAppend(tag, args, line)

		case "FETCH":
			h.handleFetch(tag, args, false)

		case "UID":
			h.handleUID(tag, args)

		case "STORE":
			h.handleStore(tag, args, false)

		case "EXPUNGE":
			if h.state != stateSelected {
				h.send(tag + " BAD Not in selected state")
				continue
			}
			h.applyExpunge(true)
			h.send(tag + " OK EXPUNGE completed")

		case "SEARCH":
			h.handleSearch(tag, args, false)

		case "CHECK":
			h.send(tag + " OK CHECK completed")

		case "CREATE", "DELETE", "RENAME":
			h.send(tag + " NO Mailbox management not supported")

		default:
			h.send(tag + " BAD Unknown command: " + cmd)
		}
	}
}

// readLine reads one CRLF-terminated line (strips the CRLF).
func (h *connHandler) readLine() (string, error) {
	line, err := h.r.ReadString('\n')
	if err != nil {
		if err == io.EOF && len(line) > 0 {
			return strings.TrimRight(line, "\r\n"), nil
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readLiteral reads exactly n bytes from the connection (used for APPEND).
func (h *connHandler) readLiteral(n int) ([]byte, error) {
	buf := make([]byte, n)
	_, err := io.ReadFull(h.r, buf)
	return buf, err
}

// parseLine splits an IMAP command line into tag, command, and remaining args.
func parseLine(line string) (tag, cmd, args string) {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		return "", "", ""
	}
	tag = parts[0]
	cmd = parts[1]
	if len(parts) == 3 {
		args = parts[2]
	}
	return tag, cmd, args
}

// requireState returns false and sends an error if the handler is not in the given state.
func (h *connHandler) requireState(tag string, needed int) bool {
	if h.state < needed {
		if needed == stateAuth {
			h.send(tag + " BAD Not authenticated")
		} else {
			h.send(tag + " BAD Not in selected state")
		}
		return false
	}
	return true
}

// ---- Command Handlers ----

func (h *connHandler) handleLogin(tag, args string) {
	// args: <username> <password>  (password may be quoted)
	parts := splitArgs(args)
	if len(parts) < 2 {
		h.send(tag + " BAD LOGIN requires username and password")
		return
	}
	username := unquote(parts[0])
	password := unquote(parts[1])

	// Validate: password must be a valid auto-login token for this username.
	valid, tokenOwner := h.authAgent.ValidateAutoLoginToken(password)
	if !valid || tokenOwner != username {
		imapLogger.PrintAndLog("IMAPNotes", "Failed login attempt for user: "+username, nil)
		h.send(tag + " NO LOGIN failed")
		return
	}

	h.username = username
	h.state = stateAuth
	imapLogger.PrintAndLog("IMAPNotes", "User logged in via IMAP: "+username, nil)
	h.send(tag + " OK LOGIN completed")
}

func (h *connHandler) handleList(tag, args string) {
	if !h.requireState(tag, stateAuth) {
		return
	}
	// We only advertise the "Notes" mailbox regardless of the reference/pattern.
	h.send(`* LIST (\HasNoChildren) "/" "Notes"`)
	h.send(tag + " OK LIST completed")
}

func (h *connHandler) handleLsub(tag, args string) {
	if !h.requireState(tag, stateAuth) {
		return
	}
	h.send(`* LSUB (\HasNoChildren) "/" "Notes"`)
	h.send(tag + " OK LSUB completed")
}

func (h *connHandler) handleStatus(tag, args string) {
	if !h.requireState(tag, stateAuth) {
		return
	}
	// args: "Notes" (MESSAGES RECENT UNSEEN UIDNEXT UIDVALIDITY)
	notes, _ := LoadAllNotes(h.database, h.userHandler, h.username)
	count := len(notes)

	uidValidity := GetUIDValidity(h.database, h.username)
	var nextUID uint32 = 1
	if count > 0 {
		nextUID = notes[count-1].UID + 1
	}

	statusItems := args
	if idx := strings.Index(args, "("); idx >= 0 {
		statusItems = args[idx:]
	}

	response := "* STATUS Notes ("
	parts := []string{}
	items := strings.ToUpper(statusItems)
	if strings.Contains(items, "MESSAGES") {
		parts = append(parts, fmt.Sprintf("MESSAGES %d", count))
	}
	if strings.Contains(items, "RECENT") {
		parts = append(parts, "RECENT 0")
	}
	if strings.Contains(items, "UNSEEN") {
		parts = append(parts, "UNSEEN 0")
	}
	if strings.Contains(items, "UIDNEXT") {
		parts = append(parts, fmt.Sprintf("UIDNEXT %d", nextUID))
	}
	if strings.Contains(items, "UIDVALIDITY") {
		parts = append(parts, fmt.Sprintf("UIDVALIDITY %d", uidValidity))
	}
	response += strings.Join(parts, " ") + ")"
	h.send(response)
	h.send(tag + " OK STATUS completed")
}

func (h *connHandler) handleSelect(tag, args string, readonly bool) {
	if !h.requireState(tag, stateAuth) {
		return
	}
	mailbox := strings.Trim(strings.TrimSpace(args), `"`)
	if !strings.EqualFold(mailbox, "notes") {
		h.send(tag + " NO Mailbox does not exist: " + mailbox)
		return
	}

	notes, _ := LoadAllNotes(h.database, h.userHandler, h.username)
	count := len(notes)
	uidValidity := GetUIDValidity(h.database, h.username)
	var nextUID uint32 = 1
	for _, n := range notes {
		if n.UID >= nextUID {
			nextUID = n.UID + 1
		}
	}

	h.send(fmt.Sprintf("* %d EXISTS", count))
	h.send("* 0 RECENT")
	h.send("* OK [UNSEEN 0]")
	h.send(fmt.Sprintf("* OK [UIDVALIDITY %d]", uidValidity))
	h.send(fmt.Sprintf("* OK [UIDNEXT %d]", nextUID))
	h.send(`* FLAGS (\Answered \Flagged \Deleted \Seen \Draft)`)
	h.send(`* OK [PERMANENTFLAGS (\Deleted \Seen \*)]`)

	h.state = stateSelected
	h.deleted = make(map[uint32]bool)
	if readonly {
		h.send(tag + " OK [READ-ONLY] EXAMINE completed")
	} else {
		h.send(tag + " OK [READ-WRITE] SELECT completed")
	}
}

func (h *connHandler) handleAppend(tag, args, fullLine string) {
	if !h.requireState(tag, stateAuth) {
		return
	}
	// args pattern: "Notes" [(\flags)] ["date"] {size}
	// We only care about the literal size at the end.
	literalSize := parseLiteralSize(args)
	if literalSize < 0 {
		h.send(tag + " BAD APPEND missing literal size")
		return
	}

	// Signal readiness to receive literal data
	h.send("+ Ready for literal data")

	msgBytes, err := h.readLiteral(literalSize)
	if err != nil {
		h.send(tag + " NO APPEND failed: read error")
		return
	}
	// Consume trailing CRLF after the literal (optional per RFC)
	h.r.ReadString('\n')

	// Generate a unique note ID
	noteID := "imap_" + strings.ReplaceAll(uuid.NewV4().String(), "-", "")[:16]

	uid, err := WriteNote(h.database, h.userHandler, h.username, noteID, string(msgBytes))
	if err != nil {
		imapLogger.PrintAndLog("IMAPNotes", "APPEND write error: "+err.Error(), err)
		h.send(tag + " NO APPEND failed: write error")
		return
	}

	uidValidity := GetUIDValidity(h.database, h.username)
	h.send(tag + fmt.Sprintf(" OK [APPENDUID %d %d] APPEND completed", uidValidity, uid))
}

func (h *connHandler) handleFetch(tag, args string, uidMode bool) {
	if !h.requireState(tag, stateSelected) {
		return
	}
	notes, _ := LoadAllNotes(h.database, h.userHandler, h.username)

	seqStr, items := splitFirst(args)
	seqSet := parseSequenceSet(seqStr, uint32(len(notes)), notes, uidMode)

	for _, n := range notes {
		var seq uint32
		for i, nn := range notes {
			if nn.UID == n.UID {
				seq = uint32(i + 1)
			}
		}
		if !seqSet[seq] {
			continue
		}
		h.sendFetchResponse(seq, n, items, uidMode)
	}
	h.send(tag + " OK FETCH completed")
}

func (h *connHandler) sendFetchResponse(seq uint32, n Note, items string, includeUID bool) {
	itemsUpper := strings.ToUpper(items)
	var parts []string

	if includeUID || strings.Contains(itemsUpper, "UID") {
		parts = append(parts, fmt.Sprintf("UID %d", n.UID))
	}
	if strings.Contains(itemsUpper, "FLAGS") {
		if h.deleted[n.UID] {
			parts = append(parts, `FLAGS (\Deleted)`)
		} else {
			parts = append(parts, "FLAGS ()")
		}
	}
	if strings.Contains(itemsUpper, "INTERNALDATE") {
		parts = append(parts, fmt.Sprintf(`INTERNALDATE "%s"`, IMAPDate(n.UpdatedAt)))
	}
	if strings.Contains(itemsUpper, "RFC822.SIZE") || strings.Contains(itemsUpper, "RFC822.SIZE") {
		msg := BuildEmailMessage(n)
		parts = append(parts, fmt.Sprintf("RFC822.SIZE %d", len([]byte(msg))))
	}
	if strings.Contains(itemsUpper, "ENVELOPE") {
		parts = append(parts, buildEnvelope(n))
	}

	// Check for body fetch requests
	needFullBody := strings.Contains(itemsUpper, "BODY[]") ||
		strings.Contains(itemsUpper, "BODY.PEEK[]") ||
		strings.Contains(itemsUpper, "RFC822") ||
		(strings.Contains(itemsUpper, "BODY[") && !strings.Contains(itemsUpper, "BODY[HEADER"))

	needHeader := strings.Contains(itemsUpper, "BODY[HEADER") ||
		strings.Contains(itemsUpper, "BODY.PEEK[HEADER")

	if needFullBody {
		msg := BuildEmailMessage(n)
		msgBytes := []byte(msg)

		bodyKey := "BODY[]"
		if strings.Contains(itemsUpper, "BODY.PEEK[]") {
			bodyKey = "BODY[]"
		} else if strings.Contains(itemsUpper, "RFC822") {
			bodyKey = "RFC822"
		}

		// Emit: * <seq> FETCH (UID x FLAGS () ... BODY[] {size}\r\n<data>)
		prefix := fmt.Sprintf("* %d FETCH (", seq)
		if len(parts) > 0 {
			prefix += strings.Join(parts, " ") + " "
		}
		prefix += fmt.Sprintf("%s {%d}", bodyKey, len(msgBytes))
		h.w.WriteString(prefix + "\r\n")
		h.w.Write(msgBytes)
		h.w.WriteString(")\r\n")
		h.w.Flush()
		return
	}

	if needHeader {
		msg := BuildEmailMessage(n)
		// Return only the header section
		headerEnd := strings.Index(msg, "\r\n\r\n")
		var headerBytes []byte
		if headerEnd >= 0 {
			headerBytes = []byte(msg[:headerEnd+4])
		} else {
			headerBytes = []byte(msg)
		}
		bodyKey := "BODY[HEADER]"
		prefix := fmt.Sprintf("* %d FETCH (", seq)
		if len(parts) > 0 {
			prefix += strings.Join(parts, " ") + " "
		}
		prefix += fmt.Sprintf("%s {%d}", bodyKey, len(headerBytes))
		h.w.WriteString(prefix + "\r\n")
		h.w.Write(headerBytes)
		h.w.WriteString(")\r\n")
		h.w.Flush()
		return
	}

	if len(parts) > 0 {
		h.send(fmt.Sprintf("* %d FETCH (%s)", seq, strings.Join(parts, " ")))
	}
}

func (h *connHandler) handleUID(tag, args string) {
	cmd, rest := splitFirst(args)
	switch strings.ToUpper(cmd) {
	case "FETCH":
		h.handleFetch(tag, rest, true)
	case "STORE":
		h.handleStore(tag, rest, true)
	case "SEARCH":
		h.handleSearch(tag, rest, true)
	case "EXPUNGE":
		if !h.requireState(tag, stateSelected) {
			return
		}
		// UID EXPUNGE <uidset> - mark those UIDs deleted and expunge them
		parts := strings.Fields(rest)
		if len(parts) > 0 {
			notes, _ := LoadAllNotes(h.database, h.userHandler, h.username)
			uids := parseUIDSet(parts[0], notes)
			for uid := range uids {
				h.deleted[uid] = true
			}
		}
		h.applyExpunge(true)
		h.send(tag + " OK UID EXPUNGE completed")
	case "COPY":
		h.send(tag + " NO UID COPY not supported")
	default:
		h.send(tag + " BAD Unknown UID sub-command: " + cmd)
	}
}

func (h *connHandler) handleStore(tag, args string, uidMode bool) {
	if !h.requireState(tag, stateSelected) {
		return
	}
	notes, _ := LoadAllNotes(h.database, h.userHandler, h.username)

	parts := strings.Fields(args)
	if len(parts) < 3 {
		h.send(tag + " BAD STORE requires seqset, flags-action, flags")
		return
	}
	seqStr := parts[0]
	action := strings.ToUpper(parts[1])
	// Collect remaining as flags: could span multiple parts
	flagStr := strings.ToUpper(strings.Join(parts[2:], " "))

	seqSet := parseSequenceSet(seqStr, uint32(len(notes)), notes, uidMode)

	for _, n := range notes {
		var seq uint32
		for i, nn := range notes {
			if nn.UID == n.UID {
				seq = uint32(i + 1)
			}
		}
		if !seqSet[seq] {
			continue
		}
		if strings.Contains(action, "+FLAGS") && strings.Contains(flagStr, `\DELETED`) {
			h.deleted[n.UID] = true
		} else if strings.Contains(action, "-FLAGS") && strings.Contains(flagStr, `\DELETED`) {
			delete(h.deleted, n.UID)
		} else if action == "FLAGS" || action == "FLAGS.SILENT" {
			if strings.Contains(flagStr, `\DELETED`) {
				h.deleted[n.UID] = true
			} else {
				delete(h.deleted, n.UID)
			}
		}

		if !strings.Contains(action, "SILENT") {
			flags := "()"
			if h.deleted[n.UID] {
				flags = `(\Deleted)`
			}
			h.send(fmt.Sprintf("* %d FETCH (FLAGS %s)", seq, flags))
		}
	}
	h.send(tag + " OK STORE completed")
}

func (h *connHandler) handleSearch(tag, args string, uidMode bool) {
	if !h.requireState(tag, stateSelected) {
		return
	}
	notes, _ := LoadAllNotes(h.database, h.userHandler, h.username)

	// Return all non-deleted messages.
	var results []string
	for i, n := range notes {
		if !h.deleted[n.UID] {
			if uidMode {
				results = append(results, strconv.FormatUint(uint64(n.UID), 10))
			} else {
				results = append(results, strconv.Itoa(i+1))
			}
		}
	}

	h.send("* SEARCH " + strings.Join(results, " "))
	h.send(tag + " OK SEARCH completed")
}

// applyExpunge deletes all notes flagged as \Deleted.
// If notify is true, sends * n EXPUNGE responses per RFC 3501.
func (h *connHandler) applyExpunge(notify bool) {
	if len(h.deleted) == 0 {
		return
	}
	notes, _ := LoadAllNotes(h.database, h.userHandler, h.username)
	deleted := make([]uint32, 0, len(h.deleted))
	for uid := range h.deleted {
		deleted = append(deleted, uid)
	}

	// Process deletions in reverse sequence order so sequence numbers stay valid
	// as per RFC 3501 §6.4.3.
	for i := len(notes) - 1; i >= 0; i-- {
		n := notes[i]
		isDeleted := false
		for _, uid := range deleted {
			if n.UID == uid {
				isDeleted = true
				break
			}
		}
		if !isDeleted {
			continue
		}
		err := DeleteNote(h.database, h.userHandler, h.username, n.UID)
		if err != nil {
			imapLogger.PrintAndLog("IMAPNotes", "Delete note error: "+err.Error(), err)
		}
		if notify {
			h.send(fmt.Sprintf("* %d EXPUNGE", i+1))
		}
	}
	h.deleted = make(map[uint32]bool)
}

// ---- IMAP Helpers ----

// buildEnvelope constructs an IMAP ENVELOPE structure for a Note.
func buildEnvelope(n Note) string {
	dateStr := RFC2822Date(n.UpdatedAt)
	if n.UpdatedAt == 0 {
		dateStr = RFC2822Date(time.Now().UnixMilli())
	}
	subject := mimeEncodeHeader(n.Title)
	addr := `(("ArozOS Notes" NIL "notes" "arozos"))`
	msgID := "<" + n.ID + "@arozos>"
	return fmt.Sprintf(
		`ENVELOPE ("%s" "%s" %s %s %s %s NIL NIL NIL "%s")`,
		dateStr, subject, addr, addr, addr, addr, msgID,
	)
}

// parseLiteralSize extracts the {n} literal size from the end of an IMAP argument string.
func parseLiteralSize(s string) int {
	start := strings.LastIndex(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < 0 || end < start {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(s[start+1 : end]))
	if err != nil {
		return -1
	}
	return n
}

// parseSequenceSet parses a message sequence set string and returns a set of
// matching sequence numbers (1-based).
// In UID mode the seqStr contains UIDs; it maps them to sequence numbers.
func parseSequenceSet(seqStr string, count uint32, notes []Note, uidMode bool) map[uint32]bool {
	result := map[uint32]bool{}
	if count == 0 {
		return result
	}

	for _, part := range strings.Split(seqStr, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, ":") {
			bounds := strings.SplitN(part, ":", 2)
			lo := parseSeqNum(bounds[0], count)
			hi := parseSeqNum(bounds[1], count)
			if lo > hi {
				lo, hi = hi, lo
			}
			for i := lo; i <= hi; i++ {
				if uidMode {
					// i is a UID; find its sequence number
					for seq, n := range notes {
						if n.UID == i {
							result[uint32(seq+1)] = true
						}
					}
				} else {
					if i >= 1 && i <= count {
						result[i] = true
					}
				}
			}
		} else {
			v := parseSeqNum(part, count)
			if uidMode {
				for seq, n := range notes {
					if n.UID == v {
						result[uint32(seq+1)] = true
					}
				}
			} else {
				if v >= 1 && v <= count {
					result[v] = true
				}
			}
		}
	}
	return result
}

// parseUIDSet returns a set of UIDs from a UID set string.
func parseUIDSet(seqStr string, notes []Note) map[uint32]bool {
	result := map[uint32]bool{}
	var maxUID uint32
	for _, n := range notes {
		if n.UID > maxUID {
			maxUID = n.UID
		}
	}
	for _, part := range strings.Split(seqStr, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, ":") {
			bounds := strings.SplitN(part, ":", 2)
			lo := parseSeqNum(bounds[0], maxUID)
			hi := parseSeqNum(bounds[1], maxUID)
			if lo > hi {
				lo, hi = hi, lo
			}
			for i := lo; i <= hi; i++ {
				result[i] = true
			}
		} else {
			v := parseSeqNum(part, maxUID)
			result[v] = true
		}
	}
	return result
}

func parseSeqNum(s string, max uint32) uint32 {
	s = strings.TrimSpace(s)
	if s == "*" {
		return max
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// splitFirst splits a string into the first space-separated token and the rest.
func splitFirst(s string) (head, tail string) {
	s = strings.TrimSpace(s)
	idx := strings.IndexByte(s, ' ')
	if idx < 0 {
		return s, ""
	}
	return s[:idx], strings.TrimSpace(s[idx+1:])
}

// splitArgs splits an argument string respecting quoted strings.
func splitArgs(s string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			inQuote = !inQuote
			current.WriteByte(c)
		} else if c == ' ' && !inQuote {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		} else {
			current.WriteByte(c)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// unquote removes surrounding double-quotes from a string.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
