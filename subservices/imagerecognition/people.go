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

// maxExemplarsPerPerson caps how many identity vectors are kept per person.
// Keeping several exemplars (rather than one averaged centroid) lets a person be
// recognised across very different poses/lighting: a new face is matched to the
// nearest stored exemplar, not to a blurred mean.
const maxExemplarsPerPerson = 16

// personRecord is the persisted state of one known person.
type personRecord struct {
	UUID       string      `json:"uuid"`
	Embeddings [][]float32 `json:"embeddings"`         //Diverse identity exemplars for this person
	Centroid   []float32   `json:"centroid,omitempty"` //Legacy single-centroid field, migrated on load
	Samples    int         `json:"samples"`            //Number of faces grouped into this person
	Created    int64       `json:"created"`            //Unix seconds
	Updated    int64       `json:"updated"`            //Unix seconds
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

// Assign groups descriptor under the best-matching known person (nearest stored
// exemplar across all people), or creates a new person if none is similar
// enough. Returns the person's UUID, whether a new person was created and the
// similarity score of the match.
func (s *PeopleStore) Assign(descriptor []float32) (string, bool, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bestIdx := -1
	bestScore := -1.0
	for i, p := range s.people {
		score := p.bestSimilarity(descriptor)
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	now := time.Now().Unix()
	if bestIdx >= 0 && bestScore >= s.threshold {
		p := s.people[bestIdx]
		p.addExemplar(descriptor)
		p.Samples++
		p.Updated = now
		s.persist()
		return p.UUID, false, roundTo(bestScore, 4)
	}

	//No sufficiently similar person; register a new one.
	p := &personRecord{
		UUID:       newUUIDv4(),
		Embeddings: [][]float32{cloneVec(descriptor)},
		Samples:    1,
		Created:    now,
		Updated:    now,
	}
	s.people = append(s.people, p)
	s.persist()
	return p.UUID, true, roundTo(maxFloat(bestScore, 0), 4)
}

// bestSimilarity returns the highest cosine similarity between descriptor and
// any of the person's stored exemplars.
func (p *personRecord) bestSimilarity(descriptor []float32) float64 {
	best := -1.0
	for _, e := range p.Embeddings {
		if sc := cosineSimilarity(descriptor, e); sc > best {
			best = sc
		}
	}
	return best
}

// addExemplar stores descriptor as a new identity exemplar. Below the cap it is
// simply appended; once full, it replaces the most-similar existing exemplar so
// the retained set stays diverse across poses rather than filling with
// near-duplicates.
func (p *personRecord) addExemplar(descriptor []float32) {
	if len(p.Embeddings) < maxExemplarsPerPerson {
		p.Embeddings = append(p.Embeddings, cloneVec(descriptor))
		return
	}
	mostSimIdx, mostSim := 0, -2.0
	for i, e := range p.Embeddings {
		if sc := cosineSimilarity(descriptor, e); sc > mostSim {
			mostSim = sc
			mostSimIdx = i
		}
	}
	p.Embeddings[mostSimIdx] = cloneVec(descriptor)
}

func cloneVec(v []float32) []float32 {
	out := make([]float32, len(v))
	copy(out, v)
	return out
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
	//Migrate legacy single-centroid records to the exemplar format.
	for _, p := range records {
		if len(p.Embeddings) == 0 && len(p.Centroid) > 0 {
			p.Embeddings = [][]float32{p.Centroid}
		}
		p.Centroid = nil
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
