package main

import "testing"

// TestRecognizeGroupsSamePersonAcrossCalls is the headline behaviour: the same
// face, re-submitted (and under a brightness change), is grouped under one
// stable person UUID, while genuinely different people get distinct UUIDs.
func TestRecognizeGroupsSamePersonAcrossCalls(t *testing.T) {
	r := newRecognizerForTest(t)
	personA := loadFixture(t, "person_a.jpg")

	//First sighting of person A creates a new group.
	first := r.RecognizeFaces(personA)
	if len(first) != 1 {
		t.Fatalf("person_a: detected %d faces, want 1", len(first))
	}
	uuidA := first[0].PersonUUID
	if uuidA == "" {
		t.Fatal("recognised face has no PersonUUID")
	}
	if !first[0].NewPerson {
		t.Error("first sighting should be flagged as a new person")
	}

	//Re-submitting the same photo must reuse the same UUID.
	again := r.RecognizeFaces(personA)
	if again[0].PersonUUID != uuidA {
		t.Errorf("same photo got UUID %s, want %s", again[0].PersonUUID, uuidA)
	}
	if again[0].NewPerson {
		t.Error("second sighting must not be flagged as new")
	}
	if again[0].MatchScore < 0.9 {
		t.Errorf("self match score %v, want >= 0.9", again[0].MatchScore)
	}

	//A global brightness change is still the same person.
	bright := r.RecognizeFaces(adjustBrightness(personA, 1.3))
	if bright[0].PersonUUID != uuidA {
		t.Errorf("brightened photo got UUID %s, want %s", bright[0].PersonUUID, uuidA)
	}

	//Two other distinct people must each get their own UUID.
	uuidB := r.RecognizeFaces(loadFixture(t, "person_b.jpg"))[0].PersonUUID
	uuidC := r.RecognizeFaces(loadFixture(t, "person_c.jpg"))[0].PersonUUID

	if uuidB == uuidA || uuidC == uuidA || uuidB == uuidC {
		t.Errorf("distinct people share UUIDs: A=%s B=%s C=%s", uuidA, uuidB, uuidC)
	}

	if got := r.people.Count(); got != 3 {
		t.Errorf("known people = %d, want 3 (A, B, C)", got)
	}
}

// TestRecognizePersistsAcrossRestart proves the gallery survives a process
// restart: a second recognizer rooted at the same data dir re-identifies a
// previously seen person under the original UUID.
func TestRecognizePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	lg := newSvcLogger("[test]")

	r1, err := NewRecognizer(dir, lg)
	if err != nil {
		t.Fatal(err)
	}
	personA := loadFixture(t, "person_a.jpg")
	uuidA := r1.RecognizeFaces(personA)[0].PersonUUID
	r1.Close()

	r2, err := NewRecognizer(dir, lg)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if got := r2.people.Count(); got != 1 {
		t.Fatalf("reloaded people = %d, want 1", got)
	}
	if got := r2.RecognizeFaces(personA)[0].PersonUUID; got != uuidA {
		t.Errorf("after restart got UUID %s, want %s", got, uuidA)
	}
}

func TestTagIncludesPeopleAndScene(t *testing.T) {
	r := newRecognizerForTest(t)
	tags := r.Tag(loadFixture(t, "person_a.jpg"))
	if len(tags) == 0 {
		t.Fatal("expected non-empty tags")
	}
	//A portrait photo with a face should be tagged as containing a person.
	if !hasTag(tags, "person") && !hasTag(tags, "people") {
		t.Errorf("expected a person/people tag in %+v", tags)
	}
}

func TestDetectFacesDoesNotAssignUUID(t *testing.T) {
	r := newRecognizerForTest(t)
	faces := r.DetectFaces(loadFixture(t, "person_a.jpg"))
	if len(faces) != 1 {
		t.Fatalf("detected %d faces, want 1", len(faces))
	}
	if faces[0].PersonUUID != "" {
		t.Errorf("DetectFaces should not assign a UUID, got %s", faces[0].PersonUUID)
	}
	//Detection alone must not create any person groups.
	if r.people.Count() != 0 {
		t.Errorf("DetectFaces created %d people, want 0", r.people.Count())
	}
}

func TestAnalyzeShape(t *testing.T) {
	r := newRecognizerForTest(t)
	res := r.Analyze(loadFixture(t, "person_b.jpg"))

	if res.Width <= 0 || res.Height <= 0 {
		t.Errorf("dimensions = %dx%d, want positive", res.Width, res.Height)
	}
	if len(res.Tags) == 0 {
		t.Error("expected tags in analyze result")
	}
	if len(res.Faces) != 1 {
		t.Fatalf("faces = %d, want 1", len(res.Faces))
	}
	if res.Faces[0].PersonUUID == "" {
		t.Error("analyze face missing PersonUUID")
	}
	if res.Backend == "" {
		t.Error("analyze result missing backend name")
	}
}
