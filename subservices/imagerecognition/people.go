package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

/*
	people.go

	The people store is the heart of the face-recognition feature. It keeps a
	persistent gallery of known people, each identified by a stable UUID and
	represented by the running mean (centroid) of the identity descriptors seen
	for that person.

	When a new face descriptor arrives, Assign finds the most similar known
	person by cosine similarity. If the best match clears the similarity
	threshold the face is grouped under that person's existing UUID and the
	centroid is updated; otherwise a brand new person (and UUID) is created.
	This is online single-link clustering, which is what lets "the same person"
	collapse to one group across many photos and across service restarts.
*/

// personRecord is the persisted state of one known person.
type personRecord struct {
	UUID     string    `json:"uuid"`
	Centroid []float32 `json:"centroid"` //Running mean of descriptors (not re-normalised)
	Samples  int       `json:"samples"`  //Number of faces merged into this person
	Created  int64     `json:"created"`  //Unix seconds
	Updated  int64     `json:"updated"`  //Unix seconds
}

// PeopleStore is a concurrency-safe gallery of known people backed by a JSON
// file on disk.
type PeopleStore struct {
	mu        sync.Mutex
	people    []*personRecord
	path      string  //Persistence file path ("" = in-memory only)
	threshold float64 //Minimum cosine similarity to be considered the same person
}

// NewPeopleStore creates a store persisted at path (loading any existing data).
// A path of "" keeps the gallery in memory only.
func NewPeopleStore(path string, threshold float64) (*PeopleStore, error) {
	s := &PeopleStore{
		people:    []*personRecord{},
		path:      path,
		threshold: threshold,
	}
	if path != "" {
		if err := s.load(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Assign groups descriptor under the best-matching known person, or creates a
// new person if none is similar enough. It returns the person's UUID, whether a
// new person was created and the similarity score of the match.
func (s *PeopleStore) Assign(descriptor []float32) (string, bool, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bestIdx := -1
	bestScore := -1.0
	for i, p := range s.people {
		score := cosineSimilarity(descriptor, p.Centroid)
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	now := time.Now().Unix()
	if bestIdx >= 0 && bestScore >= s.threshold {
		p := s.people[bestIdx]
		mergeCentroid(p, descriptor)
		p.Updated = now
		s.persist()
		return p.UUID, false, roundTo(bestScore, 4)
	}

	//No sufficiently similar person; register a new one.
	centroid := make([]float32, len(descriptor))
	copy(centroid, descriptor)
	p := &personRecord{
		UUID:     newUUIDv4(),
		Centroid: centroid,
		Samples:  1,
		Created:  now,
		Updated:  now,
	}
	s.people = append(s.people, p)
	s.persist()
	return p.UUID, true, roundTo(maxFloat(bestScore, 0), 4)
}

// List returns the UUIDs of all known people, newest last.
func (s *PeopleStore) List() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.people))
	for _, p := range s.people {
		out = append(out, p.UUID)
	}
	return out
}

// People returns a snapshot of person metadata (without descriptor vectors),
// ordered by creation time.
func (s *PeopleStore) People() []map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]interface{}, 0, len(s.people))
	snapshot := make([]*personRecord, len(s.people))
	copy(snapshot, s.people)
	sort.SliceStable(snapshot, func(i, j int) bool {
		return snapshot[i].Created < snapshot[j].Created
	})
	for _, p := range snapshot {
		out = append(out, map[string]interface{}{
			"uuid":    p.UUID,
			"samples": p.Samples,
			"created": p.Created,
			"updated": p.Updated,
		})
	}
	return out
}

// Count returns the number of known people.
func (s *PeopleStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.people)
}

// Reset clears the gallery and removes the persisted file.
func (s *PeopleStore) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.people = []*personRecord{}
	if s.path != "" {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// mergeCentroid updates p's centroid with a new descriptor using an incremental
// mean, then increments the sample count.
func mergeCentroid(p *personRecord, descriptor []float32) {
	if len(p.Centroid) != len(descriptor) {
		//First descriptor wins the dimensionality if records were empty.
		p.Centroid = append([]float32(nil), descriptor...)
		p.Samples = 1
		return
	}
	n := float64(p.Samples)
	for i := range p.Centroid {
		p.Centroid[i] = float32((float64(p.Centroid[i])*n + float64(descriptor[i])) / (n + 1))
	}
	p.Samples++
}

// persist writes the gallery to disk. Callers must hold s.mu. Errors are
// swallowed deliberately: a failed write must not break recognition, and the
// next successful Assign will re-persist.
func (s *PeopleStore) persist() {
	if s.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0775)
	data, err := json.Marshal(s.people)
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

// load reads the gallery from disk if the file exists.
func (s *PeopleStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var records []*personRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("corrupt people store %s: %w", s.path, err)
	}
	s.people = records
	return nil
}

// newUUIDv4 returns a random RFC-4122 version 4 UUID string. Implemented with
// crypto/rand to avoid an external dependency.
func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 //version 4
	b[8] = (b[8] & 0x3f) | 0x80 //variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
