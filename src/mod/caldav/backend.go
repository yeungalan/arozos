package caldav

/*
	backend.go — interface layer between the CalDAV handler and the arozos
	user/filesystem stack.

	caldavUserHandler / caldavUser / caldavAuthAgent are narrow interfaces
	used internally by Manager.  The arozosBackend / arozosUser types adapt
	the real *user.UserHandler and *user.User to those interfaces.

	Tests use the mock implementations in caldav_test.go instead.
*/

import (
	"encoding/json"
	"time"

	fs "imuslab.com/arozos/mod/filesystem"
	"imuslab.com/arozos/mod/user"
)

// caldavAuthAgent validates CalDAV Basic-Auth credentials.
type caldavAuthAgent interface {
	ValidateAutoLoginToken(token string) (bool, string)
}

// caldavUser provides per-user note storage operations.
type caldavUser interface {
	GetUsername() string
	LoadNotes() ([]noteMeta, error)
	ReadNoteContent(id string) (content string, ts int64)
	WriteNote(id, content string, ts int64) error
	DeleteNote(id string) error
}

// caldavUserHandler resolves auth and per-user note access.
type caldavUserHandler interface {
	GetAuthAgent() caldavAuthAgent
	GetUser(username string) (caldavUser, error)
}

// ─── arozos adapter ──────────────────────────────────────────────────────────

type arozosBackend struct{ h *user.UserHandler }

func (a *arozosBackend) GetAuthAgent() caldavAuthAgent { return a.h.GetAuthAgent() }

func (a *arozosBackend) GetUser(username string) (caldavUser, error) {
	u, err := a.h.GetUserInfoFromUsername(username)
	if err != nil {
		return nil, err
	}
	return &arozosUser{u: u}, nil
}

type arozosUser struct{ u *user.User }

func (a *arozosUser) GetUsername() string { return a.u.Username }

func (a *arozosUser) fsh() (*fs.FileSystemHandler, error) {
	return a.u.GetFileSystemHandlerFromVirtualPath("user:/Document/Notes")
}

func (a *arozosUser) LoadNotes() ([]noteMeta, error) {
	handler, err := a.fsh()
	if err != nil {
		return nil, err
	}
	metaReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/meta.json", a.u.Username)
	if err != nil {
		return nil, err
	}
	raw, err := handler.FileSystemAbstraction.ReadFile(metaReal)
	if err != nil {
		return []noteMeta{}, nil
	}
	var meta notesMeta
	if err2 := json.Unmarshal(raw, &meta); err2 != nil {
		return []noteMeta{}, nil
	}
	return meta.Notes, nil
}

func (a *arozosUser) ReadNoteContent(id string) (content string, ts int64) {
	handler, err := a.fsh()
	if err != nil {
		return "", 0
	}
	realPath, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/"+id+".txt", a.u.Username)
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

func (a *arozosUser) WriteNote(id, content string, ts int64) error {
	handler, err := a.fsh()
	if err != nil {
		return err
	}
	dirReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes", a.u.Username)
	if err != nil {
		return err
	}
	handler.FileSystemAbstraction.MkdirAll(dirReal, 0755)

	noteReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/"+id+".txt", a.u.Username)
	if err != nil {
		return err
	}
	if err := handler.FileSystemAbstraction.WriteFile(noteReal, []byte(content), 0644); err != nil {
		return err
	}

	metaReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/meta.json", a.u.Username)
	if err != nil {
		return err
	}
	var meta notesMeta
	if raw, err2 := handler.FileSystemAbstraction.ReadFile(metaReal); err2 == nil {
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

func (a *arozosUser) DeleteNote(id string) error {
	handler, err := a.fsh()
	if err != nil {
		return err
	}
	noteReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/"+id+".txt", a.u.Username)
	if err != nil {
		return err
	}
	_ = handler.FileSystemAbstraction.Remove(noteReal)

	metaReal, err := handler.FileSystemAbstraction.VirtualPathToRealPath("/Document/Notes/meta.json", a.u.Username)
	if err != nil {
		return err
	}
	var meta notesMeta
	if raw, err2 := handler.FileSystemAbstraction.ReadFile(metaReal); err2 == nil {
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
