package main

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "cameras.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func sampleCamera() Camera {
	return Camera{
		Name:      "Lobby",
		RTSPURL:   "rtsp://192.168.1.20:554/stream1",
		Username:  "admin",
		Password:  "hunter2",
		Transport: TransportTCP,
		Codec:     "h264",
		Tags:      []string{"lobby", "hd"},
		Enabled:   true,
		Recording: RecordingSettings{Mode: RecordingContinuous, Format: "mp4"},
	}
}

func TestAddAndRedact(t *testing.T) {
	s := newTestStore(t)
	got, err := s.AddCamera(sampleCamera())
	if err != nil {
		t.Fatalf("AddCamera: %v", err)
	}
	if got.ID == "" {
		t.Fatal("expected generated id")
	}
	if got.Password != "" {
		t.Errorf("password leaked in response: %q", got.Password)
	}
	if !got.HasPassword {
		t.Error("HasPassword should be true")
	}
	if got.Status != StatusUnknown {
		t.Errorf("new enabled camera status = %q, want unknown", got.Status)
	}
}

func TestAddInvalid(t *testing.T) {
	s := newTestStore(t)
	c := sampleCamera()
	c.RTSPURL = "not-a-url"
	if _, err := s.AddCamera(c); err == nil {
		t.Fatal("expected validation error for bad url")
	}
	if len(s.ListCameras()) != 0 {
		t.Error("invalid camera should not be stored")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cameras.json")
	s1, _ := NewStore(path)
	added, err := s1.AddCamera(sampleCamera())
	if err != nil {
		t.Fatalf("AddCamera: %v", err)
	}

	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := s2.GetCamera(added.ID)
	if err != nil {
		t.Fatalf("GetCamera after reload: %v", err)
	}
	if got.Name != "Lobby" {
		t.Errorf("reloaded name = %q", got.Name)
	}
	// Password must survive reload internally even though it is redacted in the API.
	raw, ok := s2.getRaw(added.ID)
	if !ok || raw.Password != "hunter2" {
		t.Errorf("password not persisted: ok=%v pw=%q", ok, raw.Password)
	}
}

func TestUpdatePreservesPassword(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.AddCamera(sampleCamera())

	upd := sampleCamera()
	upd.Name = "Lobby West"
	upd.Password = "" // client did not resend the secret
	got, err := s.UpdateCamera(added.ID, upd)
	if err != nil {
		t.Fatalf("UpdateCamera: %v", err)
	}
	if got.Name != "Lobby West" {
		t.Errorf("name not updated: %q", got.Name)
	}
	raw, _ := s.getRaw(added.ID)
	if raw.Password != "hunter2" {
		t.Errorf("password should be preserved, got %q", raw.Password)
	}
}

func TestEnableDisable(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.AddCamera(sampleCamera())
	off, err := s.SetEnabled(added.ID, false)
	if err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if off.Enabled || off.Status != StatusDisabled {
		t.Errorf("disable failed: enabled=%v status=%q", off.Enabled, off.Status)
	}
	on, _ := s.SetEnabled(added.ID, true)
	if !on.Enabled || on.Status != StatusUnknown {
		t.Errorf("enable failed: enabled=%v status=%q", on.Enabled, on.Status)
	}
}

func TestDeleteCamera(t *testing.T) {
	s := newTestStore(t)
	added, _ := s.AddCamera(sampleCamera())
	if err := s.DeleteCamera(added.ID); err != nil {
		t.Fatalf("DeleteCamera: %v", err)
	}
	if _, err := s.GetCamera(added.ID); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	if err := s.DeleteCamera("missing"); err != ErrNotFound {
		t.Errorf("delete of missing id = %v, want ErrNotFound", err)
	}
}

func TestGroupsAndCascade(t *testing.T) {
	s := newTestStore(t)
	g, err := s.AddGroup(Group{Name: "Ground floor", Kind: "floor"})
	if err != nil {
		t.Fatalf("AddGroup: %v", err)
	}
	c := sampleCamera()
	c.GroupID = g.ID
	cam, err := s.AddCamera(c)
	if err != nil {
		t.Fatalf("AddCamera with group: %v", err)
	}

	// Camera referencing a non-existent group must be rejected.
	bad := sampleCamera()
	bad.GroupID = "nope"
	if _, err := s.AddCamera(bad); err == nil {
		t.Error("expected error for camera with unknown group")
	}

	// Deleting a group clears the reference but keeps the camera.
	if err := s.DeleteGroup(g.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	got, err := s.GetCamera(cam.ID)
	if err != nil {
		t.Fatalf("GetCamera after group delete: %v", err)
	}
	if got.GroupID != "" {
		t.Errorf("camera group not cleared after group delete: %q", got.GroupID)
	}
}

func TestSearchAndTags(t *testing.T) {
	s := newTestStore(t)
	c1 := sampleCamera()
	c1.Name = "Parking North"
	c1.Manufacturer = "Hikvision"
	c1.Tags = []string{"outdoor", "parking"}
	s.AddCamera(c1)

	c2 := sampleCamera()
	c2.Name = "Server Room"
	c2.Manufacturer = "Axis"
	c2.Tags = []string{"indoor"}
	s.AddCamera(c2)

	if got := s.SearchCameras(SearchQuery{Text: "parking"}); len(got) != 1 || got[0].Name != "Parking North" {
		t.Errorf("text search failed: %+v", got)
	}
	if got := s.SearchCameras(SearchQuery{Manufacturer: "axis"}); len(got) != 1 || got[0].Name != "Server Room" {
		t.Errorf("manufacturer search failed: %+v", got)
	}
	if got := s.SearchCameras(SearchQuery{Tag: "outdoor"}); len(got) != 1 {
		t.Errorf("tag search failed: %+v", got)
	}
	tags := s.Tags()
	if len(tags) != 3 {
		t.Errorf("expected 3 distinct tags, got %v", tags)
	}
}
