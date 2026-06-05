package applenotes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NoteMeta matches the entry format in arozos Notes meta.json.
type NoteMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
}

// NotesMeta is the full structure of meta.json.
type NotesMeta struct {
	LastOpened string     `json:"lastOpened"`
	Theme      string     `json:"theme"`
	Notes      []NoteMeta `json:"notes"`
}

// PerformSync executes a full bidirectional sync between Apple Notes and arozos.
// notesDir is the absolute filesystem path to the user's Notes directory.
func PerformSync(cfg SyncConfig, notesDir string, prevState *SyncState) (*SyncResult, *SyncState, error) {
	result := &SyncResult{Timestamp: time.Now()}

	ic, err := Connect(cfg)
	if err != nil {
		result.Message = fmt.Sprintf("IMAP connection failed: %v", err)
		return result, prevState, err
	}
	defer ic.Close()

	// ── 1. Load remote notes ──────────────────────────────────────────────
	appleNotes, err := ic.ListNotes()
	if err != nil {
		result.Message = fmt.Sprintf("Failed to list Apple Notes: %v", err)
		return result, prevState, err
	}

	// ── 2. Load local notes ───────────────────────────────────────────────
	localMeta, err := loadLocalMeta(notesDir)
	if err != nil {
		result.Message = fmt.Sprintf("Failed to read local notes: %v", err)
		return result, prevState, err
	}

	// Build lookup maps
	appleByUID := make(map[uint32]*AppleNote, len(appleNotes))
	for _, n := range appleNotes {
		appleByUID[n.UID] = n
	}

	prevMappings := make(map[string]*NoteMapping) // localID → mapping
	prevByUID := make(map[uint32]*NoteMapping)    // imapUID → mapping
	if prevState != nil {
		for i := range prevState.Mappings {
			m := &prevState.Mappings[i]
			prevMappings[m.LocalID] = m
			prevByUID[m.IMAPUID] = m
		}
	}

	newState := &SyncState{LastSync: time.Now()}
	lastSync := time.Time{}
	if prevState != nil {
		lastSync = prevState.LastSync
	}

	// ── 3. Apple → arozos (new and updated) ──────────────────────────────
	for _, an := range appleNotes {
		if mapping, seen := prevByUID[an.UID]; seen {
			// We've seen this note before — check if it changed on Apple's side.
			if an.Date.After(lastSync) && an.Date.After(mapping.IMAPDate) {
				// Apple updated it since last sync — overwrite local copy.
				localID := mapping.LocalID
				plainText := an.PlainText()
				if err := writeLocalNote(notesDir, localID, plainText); err == nil {
					// Update local meta
					updateLocalMetaEntry(localMeta, localID, titleFromContent(plainText), an.Date.UnixMilli())
					newState.Mappings = append(newState.Mappings, NoteMapping{
						LocalID:      localID,
						IMAPUID:      an.UID,
						LocalUpdated: an.Date.UnixMilli(),
						IMAPDate:     an.Date,
					})
					result.Updated++
					continue
				}
			}
			// No change or error — keep existing mapping.
			newState.Mappings = append(newState.Mappings, *mapping)
			// Update IMAPDate in case the message was re-appended.
			newState.Mappings[len(newState.Mappings)-1].IMAPDate = an.Date
		} else {
			// New Apple note — import to arozos.
			localID := generateLocalID()
			plainText := an.PlainText()
			if plainText == "" {
				plainText = an.Subject
			}
			if err := writeLocalNote(notesDir, localID, plainText); err != nil {
				continue
			}
			ts := an.Date.UnixMilli()
			updateLocalMetaEntry(localMeta, localID, titleFromContent(plainText), ts)
			newState.Mappings = append(newState.Mappings, NoteMapping{
				LocalID:      localID,
				IMAPUID:      an.UID,
				LocalUpdated: ts,
				IMAPDate:     an.Date,
			})
			result.Added++
		}
	}

	// ── 4. Apple → arozos (deletions) ────────────────────────────────────
	if prevState != nil {
		for _, m := range prevState.Mappings {
			if _, stillExists := appleByUID[m.IMAPUID]; !stillExists {
				// Note deleted on Apple side — remove locally.
				deleteLocalNote(notesDir, localMeta, m.LocalID)
				result.Deleted++
				// Don't add to newState.Mappings — it's gone.
			}
		}
	}

	// ── 5. arozos → Apple (new notes) ────────────────────────────────────
	for _, lm := range localMeta.Notes {
		if _, alreadySynced := prevMappings[lm.ID]; alreadySynced {
			continue
		}
		content, err := readLocalNote(notesDir, lm.ID)
		if err != nil {
			continue
		}
		an := &AppleNote{
			Subject:  lm.Title,
			HTMLBody: PlainTextToHTML(content),
			Date:     time.UnixMilli(lm.UpdatedAt),
		}
		uid, err := ic.CreateNote(an)
		if err != nil {
			continue
		}
		newState.Mappings = append(newState.Mappings, NoteMapping{
			LocalID:      lm.ID,
			IMAPUID:      uid,
			LocalUpdated: lm.UpdatedAt,
			IMAPDate:     time.UnixMilli(lm.UpdatedAt),
		})
		result.AppleAdded++
	}

	// ── 6. arozos → Apple (updated notes) ────────────────────────────────
	for i, m := range newState.Mappings {
		lm := findLocalMeta(localMeta, m.LocalID)
		if lm == nil {
			continue
		}
		if lm.UpdatedAt <= m.LocalUpdated {
			continue // not modified locally
		}
		// Local note modified since last sync — push to Apple.
		content, err := readLocalNote(notesDir, m.LocalID)
		if err != nil {
			continue
		}
		an := &AppleNote{
			Subject:  lm.Title,
			HTMLBody: PlainTextToHTML(content),
			Date:     time.UnixMilli(lm.UpdatedAt),
		}
		newUID, err := ic.UpdateNote(m.IMAPUID, an)
		if err != nil {
			continue
		}
		newState.Mappings[i].IMAPUID = newUID
		newState.Mappings[i].LocalUpdated = lm.UpdatedAt
		newState.Mappings[i].IMAPDate = time.UnixMilli(lm.UpdatedAt)
		result.AppleUpdated++
	}

	// ── 7. arozos → Apple (deleted notes) ────────────────────────────────
	if prevState != nil {
		localIDs := make(map[string]bool, len(localMeta.Notes))
		for _, lm := range localMeta.Notes {
			localIDs[lm.ID] = true
		}
		for _, m := range prevState.Mappings {
			if !localIDs[m.LocalID] {
				// Deleted locally — remove from Apple too.
				if _, stillOnApple := appleByUID[m.IMAPUID]; stillOnApple {
					_ = ic.DeleteNote(m.IMAPUID)
					result.AppleDeleted++
				}
			}
		}
	}

	// ── 8. Persist local meta ─────────────────────────────────────────────
	if err := saveLocalMeta(notesDir, localMeta); err != nil {
		result.Message = fmt.Sprintf("Warning: failed to save local meta: %v", err)
	}

	result.Success = true
	if result.Message == "" {
		result.Message = fmt.Sprintf(
			"Sync complete: +%d ↓  ~%d ↓  -%d ↓  | +%d ↑  ~%d ↑  -%d ↑",
			result.Added, result.Updated, result.Deleted,
			result.AppleAdded, result.AppleUpdated, result.AppleDeleted,
		)
	}
	return result, newState, nil
}

// ── local file helpers ────────────────────────────────────────────────────────

func loadLocalMeta(notesDir string) (*NotesMeta, error) {
	metaPath := filepath.Join(notesDir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if os.IsNotExist(err) {
		return &NotesMeta{Theme: "dark", Notes: []NoteMeta{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var m NotesMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return &NotesMeta{Theme: "dark", Notes: []NoteMeta{}}, nil
	}
	if m.Notes == nil {
		m.Notes = []NoteMeta{}
	}
	return &m, nil
}

func saveLocalMeta(notesDir string, m *NotesMeta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(notesDir, "meta.json"), data, 0644)
}

func writeLocalNote(notesDir, id, content string) error {
	return os.WriteFile(filepath.Join(notesDir, id+".txt"), []byte(content), 0644)
}

func readLocalNote(notesDir, id string) (string, error) {
	data, err := os.ReadFile(filepath.Join(notesDir, id+".txt"))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func deleteLocalNote(notesDir string, m *NotesMeta, id string) {
	_ = os.Remove(filepath.Join(notesDir, id+".txt"))
	for i, n := range m.Notes {
		if n.ID == id {
			m.Notes = append(m.Notes[:i], m.Notes[i+1:]...)
			return
		}
	}
}

func updateLocalMetaEntry(m *NotesMeta, id, title string, updatedAt int64) {
	for i, n := range m.Notes {
		if n.ID == id {
			m.Notes[i].Title = title
			m.Notes[i].UpdatedAt = updatedAt
			return
		}
	}
	m.Notes = append(m.Notes, NoteMeta{ID: id, Title: title, UpdatedAt: updatedAt})
}

func findLocalMeta(m *NotesMeta, id string) *NoteMeta {
	for i := range m.Notes {
		if m.Notes[i].ID == id {
			return &m.Notes[i]
		}
	}
	return nil
}

func titleFromContent(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 60 {
			return line[:60]
		}
		return line
	}
	return "New Note"
}

func generateLocalID() string {
	return fmt.Sprintf("apple_%d", time.Now().UnixNano())
}
