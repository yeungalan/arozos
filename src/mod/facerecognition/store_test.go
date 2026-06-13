package facerecognition

import (
	"path/filepath"
	"strings"
	"testing"

	db "imuslab.com/arozos/mod/database"
)

// newTestManager creates a manager backed by a throw-away database
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	database, err := db.NewDatabase(filepath.Join(t.TempDir(), "test.db"), false)
	if err != nil {
		t.Fatalf("unable to create test database: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	manager, err := NewManager(&Options{Database: database})
	if err != nil {
		t.Fatalf("unable to create manager: %v", err)
	}
	return manager
}

// makeDescriptor builds a deterministic descriptor with all the weight of
// every cell histogram inside a single bin selected by seed.
func makeDescriptor(seed int) []float32 {
	descriptor := make([]float32, DescriptorLength)
	for cell := 0; cell < descriptorGrid*descriptorGrid; cell++ {
		descriptor[cell*descriptorBins+(seed%descriptorBins)] = 1.0
	}
	return descriptor
}

// makeFace wraps a descriptor into a DetectedFace like the detector would
func makeFace(descriptor []float32) *DetectedFace {
	return &DetectedFace{X: 1, Y: 2, W: 30, H: 30, Quality: 9.5, descriptor: descriptor}
}

func TestNewManagerRequiresDatabase(t *testing.T) {
	tests := []struct {
		name    string
		options *Options
	}{
		{"nil options", nil},
		{"nil database", &Options{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewManager(tc.options); err == nil {
				t.Errorf("NewManager(%v) succeeded, want error", tc.options)
			}
		})
	}
}

func TestConfigDefaultsAndPersistence(t *testing.T) {
	manager := newTestManager(t)

	cfg := manager.GetConfig()
	if cfg.Enabled {
		t.Errorf("feature must default to disabled (optional feature)")
	}
	if cfg != DefaultConfig() {
		t.Errorf("GetConfig() = %+v, want defaults %+v", cfg, DefaultConfig())
	}

	cfg.Enabled = true
	cfg.MinFaceSize = 80
	cfg.MatchThreshold = 0.5
	if err := manager.SetConfig(cfg); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	loaded := manager.GetConfig()
	if !loaded.Enabled || loaded.MinFaceSize != 80 || loaded.MatchThreshold != 0.5 {
		t.Errorf("config did not round-trip, got %+v", loaded)
	}
	if !manager.Enabled() {
		t.Errorf("Enabled() = false after enabling the feature")
	}
}

func TestSanitizeConfig(t *testing.T) {
	tests := []struct {
		name string
		in   Config
		want Config
	}{
		{
			"too small face size clamps up",
			Config{MinFaceSize: 1, MatchThreshold: 0.3},
			Config{MinFaceSize: 20, MatchThreshold: 0.3},
		},
		{
			"too large face size clamps down",
			Config{MinFaceSize: 10000, MatchThreshold: 0.3},
			Config{MinFaceSize: 500, MatchThreshold: 0.3},
		},
		{
			"zero threshold resets to default",
			Config{MinFaceSize: 60, MatchThreshold: 0},
			Config{MinFaceSize: 60, MatchThreshold: DefaultConfig().MatchThreshold},
		},
		{
			"oversized threshold clamps to one",
			Config{MinFaceSize: 60, MatchThreshold: 42},
			Config{MinFaceSize: 60, MatchThreshold: 1},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeConfig(tc.in); got != tc.want {
				t.Errorf("sanitizeConfig(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestStoreAndClusterFaces(t *testing.T) {
	manager := newTestManager(t)
	threshold := DefaultConfig().MatchThreshold

	//Two photos with the same face descriptor: must become ONE person
	entry1 := &PhotoFaces{VPath: "user:/Photo/a.jpg", FileSize: 100, ModTime: 1000}
	if err := manager.StorePhotoFaces("alice", entry1, []*DetectedFace{makeFace(makeDescriptor(3))}, threshold); err != nil {
		t.Fatalf("StorePhotoFaces failed: %v", err)
	}
	entry2 := &PhotoFaces{VPath: "user:/Photo/b.jpg", FileSize: 200, ModTime: 2000}
	if err := manager.StorePhotoFaces("alice", entry2, []*DetectedFace{makeFace(makeDescriptor(3))}, threshold); err != nil {
		t.Fatalf("StorePhotoFaces failed: %v", err)
	}

	people := manager.ListPeople("alice")
	if len(people) != 1 {
		t.Fatalf("got %d people for two identical faces, want 1", len(people))
	}
	if people[0].FaceCount != 2 {
		t.Errorf("person face count = %d, want 2", people[0].FaceCount)
	}

	//A very different descriptor must open a second person
	entry3 := &PhotoFaces{VPath: "user:/Photo/c.jpg", FileSize: 300, ModTime: 3000}
	if err := manager.StorePhotoFaces("alice", entry3, []*DetectedFace{makeFace(makeDescriptor(40))}, threshold); err != nil {
		t.Fatalf("StorePhotoFaces failed: %v", err)
	}
	people = manager.ListPeople("alice")
	if len(people) != 2 {
		t.Fatalf("got %d people after adding a different face, want 2", len(people))
	}

	//Biggest cluster must be listed first
	if people[0].FaceCount < people[1].FaceCount {
		t.Errorf("people not sorted by cluster size: %d before %d", people[0].FaceCount, people[1].FaceCount)
	}
}

func TestRescanRebalancesPeople(t *testing.T) {
	manager := newTestManager(t)
	threshold := DefaultConfig().MatchThreshold

	entry := &PhotoFaces{VPath: "user:/Photo/a.jpg", FileSize: 100, ModTime: 1000}
	if err := manager.StorePhotoFaces("alice", entry, []*DetectedFace{makeFace(makeDescriptor(3))}, threshold); err != nil {
		t.Fatalf("StorePhotoFaces failed: %v", err)
	}
	if len(manager.ListPeople("alice")) != 1 {
		t.Fatalf("expected 1 person after first scan")
	}

	//Rescan of the same photo now finds no face: the person must disappear
	rescan := &PhotoFaces{VPath: "user:/Photo/a.jpg", FileSize: 150, ModTime: 1500}
	if err := manager.StorePhotoFaces("alice", rescan, []*DetectedFace{}, threshold); err != nil {
		t.Fatalf("StorePhotoFaces failed: %v", err)
	}
	if got := len(manager.ListPeople("alice")); got != 0 {
		t.Errorf("got %d people after rescan with no faces, want 0", got)
	}
}

func TestNeedsScan(t *testing.T) {
	manager := newTestManager(t)
	threshold := DefaultConfig().MatchThreshold

	entry := &PhotoFaces{VPath: "user:/Photo/a.jpg", FileSize: 100, ModTime: 1000}
	if err := manager.StorePhotoFaces("alice", entry, []*DetectedFace{}, threshold); err != nil {
		t.Fatalf("StorePhotoFaces failed: %v", err)
	}

	tests := []struct {
		name     string
		username string
		vpath    string
		filesize int64
		modtime  int64
		want     bool
	}{
		{"unchanged photo skips", "alice", "user:/Photo/a.jpg", 100, 1000, false},
		{"changed size rescans", "alice", "user:/Photo/a.jpg", 101, 1000, true},
		{"changed modtime rescans", "alice", "user:/Photo/a.jpg", 100, 1001, true},
		{"unknown photo scans", "alice", "user:/Photo/new.jpg", 100, 1000, true},
		{"other user scans", "bob", "user:/Photo/a.jpg", 100, 1000, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := manager.NeedsScan(tc.username, tc.vpath, tc.filesize, tc.modtime); got != tc.want {
				t.Errorf("NeedsScan() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRemovePhoto(t *testing.T) {
	manager := newTestManager(t)
	threshold := DefaultConfig().MatchThreshold

	entry := &PhotoFaces{VPath: "user:/Photo/a.jpg", FileSize: 100, ModTime: 1000}
	if err := manager.StorePhotoFaces("alice", entry, []*DetectedFace{makeFace(makeDescriptor(3))}, threshold); err != nil {
		t.Fatalf("StorePhotoFaces failed: %v", err)
	}

	manager.RemovePhoto("alice", "user:/Photo/a.jpg")
	if manager.GetPhotoFaces("alice", "user:/Photo/a.jpg") != nil {
		t.Errorf("photo entry still exists after RemovePhoto")
	}
	if got := len(manager.ListPeople("alice")); got != 0 {
		t.Errorf("got %d people after removing their only photo, want 0", got)
	}
}

func TestRenamePerson(t *testing.T) {
	manager := newTestManager(t)
	threshold := DefaultConfig().MatchThreshold

	entry := &PhotoFaces{VPath: "user:/Photo/a.jpg", FileSize: 100, ModTime: 1000}
	if err := manager.StorePhotoFaces("alice", entry, []*DetectedFace{makeFace(makeDescriptor(3))}, threshold); err != nil {
		t.Fatalf("StorePhotoFaces failed: %v", err)
	}
	personID := manager.ListPeople("alice")[0].ID

	tests := []struct {
		name     string
		personID string
		newName  string
		wantErr  bool
		wantName string
	}{
		{"simple rename", personID, "Mum", false, "Mum"},
		{"name is trimmed", personID, "  Dad  ", false, "Dad"},
		{"long name truncated", personID, strings.Repeat("x", 100), false, strings.Repeat("x", 64)},
		{"empty resets to default", personID, "", false, "Person " + personID},
		{"unknown person fails", "999", "Ghost", true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := manager.RenamePerson("alice", tc.personID, tc.newName)
			if tc.wantErr {
				if err == nil {
					t.Errorf("RenamePerson succeeded, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("RenamePerson failed: %v", err)
			}
			person := manager.GetPerson("alice", tc.personID)
			if person == nil {
				t.Fatalf("person disappeared after rename")
			}
			if person.DisplayName() != tc.wantName {
				t.Errorf("DisplayName() = %q, want %q", person.DisplayName(), tc.wantName)
			}
		})
	}
}

func TestListPersonPhotos(t *testing.T) {
	manager := newTestManager(t)
	threshold := DefaultConfig().MatchThreshold

	//Same person in two photos, second person in one photo
	for i, vpath := range []string{"user:/Photo/a.jpg", "user:/Photo/b.jpg"} {
		entry := &PhotoFaces{VPath: vpath, FileSize: int64(100 + i), ModTime: int64(1000 + i)}
		if err := manager.StorePhotoFaces("alice", entry, []*DetectedFace{makeFace(makeDescriptor(3))}, threshold); err != nil {
			t.Fatalf("StorePhotoFaces failed: %v", err)
		}
	}
	other := &PhotoFaces{VPath: "user:/Photo/c.jpg", FileSize: 300, ModTime: 3000}
	if err := manager.StorePhotoFaces("alice", other, []*DetectedFace{makeFace(makeDescriptor(40))}, threshold); err != nil {
		t.Fatalf("StorePhotoFaces failed: %v", err)
	}

	people := manager.ListPeople("alice")
	if len(people) != 2 {
		t.Fatalf("got %d people, want 2", len(people))
	}

	photos := manager.ListPersonPhotos("alice", people[0].ID)
	if len(photos) != 2 {
		t.Fatalf("got %d photos for the main person, want 2", len(photos))
	}
	//Newest photo first
	if photos[0].ModTime < photos[1].ModTime {
		t.Errorf("person photos not sorted newest first")
	}

	if got := len(manager.ListPersonPhotos("alice", "does-not-exist")); got != 0 {
		t.Errorf("got %d photos for unknown person, want 0", got)
	}
}

func TestUserStatsAndClear(t *testing.T) {
	manager := newTestManager(t)
	threshold := DefaultConfig().MatchThreshold

	aliceEntry := &PhotoFaces{VPath: "user:/Photo/a.jpg", FileSize: 100, ModTime: 1000}
	if err := manager.StorePhotoFaces("alice", aliceEntry, []*DetectedFace{makeFace(makeDescriptor(3)), makeFace(makeDescriptor(40))}, threshold); err != nil {
		t.Fatalf("StorePhotoFaces failed: %v", err)
	}
	bobEntry := &PhotoFaces{VPath: "user:/Photo/b.jpg", FileSize: 200, ModTime: 2000}
	if err := manager.StorePhotoFaces("bob", bobEntry, []*DetectedFace{makeFace(makeDescriptor(7))}, threshold); err != nil {
		t.Fatalf("StorePhotoFaces failed: %v", err)
	}

	stats := manager.GetUserStats("alice")
	if stats.ScannedPhotos != 1 || stats.TotalFaces != 2 || stats.People != 2 {
		t.Errorf("alice stats = %+v, want 1 photo / 2 faces / 2 people", stats)
	}

	//Clearing alice must not touch bob
	if err := manager.ClearUserData("alice"); err != nil {
		t.Fatalf("ClearUserData failed: %v", err)
	}
	if stats := manager.GetUserStats("alice"); stats.ScannedPhotos != 0 || stats.People != 0 {
		t.Errorf("alice stats after clear = %+v, want empty", stats)
	}
	if stats := manager.GetUserStats("bob"); stats.ScannedPhotos != 1 || stats.People != 1 {
		t.Errorf("bob stats after clearing alice = %+v, want untouched", stats)
	}

	//ClearAllData wipes everyone
	if err := manager.ClearAllData(); err != nil {
		t.Fatalf("ClearAllData failed: %v", err)
	}
	if stats := manager.GetUserStats("bob"); stats.ScannedPhotos != 0 || stats.People != 0 {
		t.Errorf("bob stats after ClearAllData = %+v, want empty", stats)
	}
}

func TestPersonIsolationBetweenUsers(t *testing.T) {
	manager := newTestManager(t)
	threshold := DefaultConfig().MatchThreshold

	//The same face descriptor for two different users must create two
	//separate people: face data is strictly per user.
	for _, username := range []string{"alice", "bob"} {
		entry := &PhotoFaces{VPath: "user:/Photo/a.jpg", FileSize: 100, ModTime: 1000}
		if err := manager.StorePhotoFaces(username, entry, []*DetectedFace{makeFace(makeDescriptor(3))}, threshold); err != nil {
			t.Fatalf("StorePhotoFaces failed: %v", err)
		}
	}
	if got := len(manager.ListPeople("alice")); got != 1 {
		t.Errorf("alice has %d people, want 1", got)
	}
	if got := len(manager.ListPeople("bob")); got != 1 {
		t.Errorf("bob has %d people, want 1", got)
	}
	if manager.GetPerson("alice", "1") == nil || manager.GetPerson("bob", "1") == nil {
		t.Errorf("per-user people not found under their own keys")
	}
}
