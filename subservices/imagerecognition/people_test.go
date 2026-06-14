package main

import (
	"os"
	"path/filepath"
	"testing"
)

// unit vectors with known relationships for deterministic clustering tests.
var (
	vecA     = []float32{1, 0, 0, 0}
	vecADup  = []float32{1, 0, 0, 0}       //identical to vecA  (cos 1.0)
	vecANear = []float32{0.95, 0.31, 0, 0} //close to vecA      (cos ~0.95)
	vecB     = []float32{0, 1, 0, 0}       //orthogonal to vecA (cos 0)
)

func TestPeopleStoreAssignNewAndMatch(t *testing.T) {
	s, err := NewPeopleStore("", 0.8)
	if err != nil {
		t.Fatal(err)
	}

	uuidA, isNew, _ := s.Assign(vecA)
	if !isNew {
		t.Fatal("first face should create a new person")
	}

	//Identical descriptor groups under the same person.
	gotUUID, isNew, score := s.Assign(vecADup)
	if isNew || gotUUID != uuidA {
		t.Errorf("identical face: got (%s,new=%v), want (%s,new=false)", gotUUID, isNew, uuidA)
	}
	if score < 0.99 {
		t.Errorf("identical face score = %v, want ~1.0", score)
	}

	//A near-duplicate above threshold also groups under the same person.
	gotUUID, isNew, _ = s.Assign(vecANear)
	if isNew || gotUUID != uuidA {
		t.Errorf("near face: got (%s,new=%v), want (%s,new=false)", gotUUID, isNew, uuidA)
	}

	if s.Count() != 1 {
		t.Errorf("count = %d, want 1 (all merged)", s.Count())
	}
}

func TestPeopleStoreDifferentPeople(t *testing.T) {
	s, _ := NewPeopleStore("", 0.8)
	uuidA, _, _ := s.Assign(vecA)
	uuidB, isNew, _ := s.Assign(vecB)

	if !isNew {
		t.Error("orthogonal face should create a new person")
	}
	if uuidA == uuidB {
		t.Errorf("different people share UUID %s", uuidA)
	}
	if s.Count() != 2 {
		t.Errorf("count = %d, want 2", s.Count())
	}
	if list := s.List(); len(list) != 2 {
		t.Errorf("List len = %d, want 2", len(list))
	}
}

func TestPeopleStorePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "people.json")

	s1, err := NewPeopleStore(path, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	uuidA, _, _ := s1.Assign(vecA)
	uuidB, _, _ := s1.Assign(vecB)

	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("people store file not written: %v", statErr)
	}

	//A fresh store at the same path must reload the gallery from disk...
	s2, err := NewPeopleStore(path, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Count() != 2 {
		t.Fatalf("reloaded count = %d, want 2", s2.Count())
	}

	//...and recognise the same person under the original UUID across "restart".
	gotUUID, isNew, _ := s2.Assign(vecADup)
	if isNew {
		t.Error("known person treated as new after reload")
	}
	if gotUUID != uuidA {
		t.Errorf("reloaded match = %s, want %s", gotUUID, uuidA)
	}
	_ = uuidB
}

func TestPeopleStoreReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "people.json")
	s, _ := NewPeopleStore(path, 0.8)
	s.Assign(vecA)
	s.Assign(vecB)

	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if s.Count() != 0 {
		t.Errorf("count after reset = %d, want 0", s.Count())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("people store file should be removed after reset")
	}
}

func TestNewUUIDv4Format(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		u := newUUIDv4()
		if len(u) != 36 {
			t.Fatalf("uuid %q length = %d, want 36", u, len(u))
		}
		if u[14] != '4' {
			t.Errorf("uuid %q version nibble = %c, want 4", u, u[14])
		}
		if seen[u] {
			t.Fatalf("duplicate uuid generated: %s", u)
		}
		seen[u] = true
	}
}
