package facerecognition

/*
	store.go

	Persistence layer for detected faces and people clusters.

	Everything is stored per user inside the system database:
	  facerecog_faces  : username/sha256(vpath) -> PhotoFaces
	  facerecog_people : username/personID      -> Person

	People are built incrementally: every new face is compared against the
	centroid descriptor of each existing person of that user; the face joins
	the closest person when the distance is below the configured threshold,
	otherwise a new person is created. Centroids are running averages so the
	cluster adapts as more faces are added.
*/

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"sort"
	"strconv"
	"strings"
)

var errPersonNotFound = errors.New("person not found")

// StoredFace is one face of one photo as persisted in the database
type StoredFace struct {
	PersonID string  `json:"personId"`
	X        int     `json:"x"`
	Y        int     `json:"y"`
	W        int     `json:"w"`
	H        int     `json:"h"`
	Quality  float64 `json:"quality"`
	Thumb    string  `json:"thumb"` //base64 JPEG crop used as avatar

	Descriptor []float32 `json:"descriptor"`
}

// PhotoFaces holds every face found in a single photo
type PhotoFaces struct {
	VPath     string       `json:"vpath"`
	FileSize  int64        `json:"filesize"`
	ModTime   int64        `json:"modtime"`
	ScannedAt int64        `json:"scannedAt"`
	Signature string       `json:"signature"` //Engine+model the descriptors were built with
	Faces     []StoredFace `json:"faces"`
}

// Person is one people cluster of a user
type Person struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	FaceCount int       `json:"faceCount"`
	Thumb     string    `json:"thumb"`     //Avatar, base64 JPEG of the first face
	Signature string    `json:"signature"` //Engine+model the centroid was built with
	Centroid  []float32 `json:"centroid"`
}

// photoKey builds the database key of one photo of one user
func photoKey(username string, vpath string) string {
	hash := sha256.Sum256([]byte(vpath))
	return username + "/" + hex.EncodeToString(hash[:])
}

// personKey builds the database key of one person of one user
func personKey(username string, personID string) string {
	return username + "/" + personID
}

// GetPhotoFaces returns the stored entry of a photo, or nil when the photo
// has not been scanned yet
func (m *Manager) GetPhotoFaces(username string, vpath string) *PhotoFaces {
	key := photoKey(username, vpath)
	if !m.options.Database.KeyExists(facesTable, key) {
		return nil
	}
	entry := PhotoFaces{}
	if err := m.options.Database.Read(facesTable, key, &entry); err != nil {
		return nil
	}
	return &entry
}

// NeedsScan reports whether the photo must be (re)scanned, comparing the
// stored file size and modification time against the current ones.
func (m *Manager) NeedsScan(username string, vpath string, filesize int64, modtime int64) bool {
	entry := m.GetPhotoFaces(username, vpath)
	if entry == nil {
		return true
	}
	return entry.FileSize != filesize || entry.ModTime != modtime
}

// StorePhotoFaces persists the scan result of one photo, assigning each face
// to a person cluster using the given matcher. A previous entry for the same
// photo is replaced and its people counters are rebalanced. Caller must hold
// the user scan lock.
func (m *Manager) StorePhotoFaces(username string, entry *PhotoFaces, faces []*DetectedFace, mt matcher) error {
	//Drop the faces of a previous scan of this photo from the clusters
	if previous := m.GetPhotoFaces(username, entry.VPath); previous != nil {
		m.detachFaces(username, previous)
	}

	entry.Signature = mt.signature
	people := m.ListPeople(username)
	entry.Faces = []StoredFace{}
	for _, face := range faces {
		person := m.assignToPerson(username, people, face.descriptor, mt)
		//Re-read the (possibly updated) people list reference for next faces
		people = m.ListPeople(username)

		stored := StoredFace{
			PersonID:   person.ID,
			X:          face.X,
			Y:          face.Y,
			W:          face.W,
			H:          face.H,
			Quality:    face.Quality,
			Thumb:      encodeThumb(face.thumb),
			Descriptor: face.descriptor,
		}
		entry.Faces = append(entry.Faces, stored)

		//First face of a fresh person doubles as its avatar
		if person.Thumb == "" && stored.Thumb != "" {
			person.Thumb = stored.Thumb
			m.options.Database.Write(peopleTable, personKey(username, person.ID), person)
		}
	}

	return m.options.Database.Write(facesTable, photoKey(username, entry.VPath), entry)
}

// RemovePhoto deletes the stored faces of a photo (e.g. the file was
// removed) and rebalances the people counters. Caller must hold the user
// scan lock.
func (m *Manager) RemovePhoto(username string, vpath string) {
	if previous := m.GetPhotoFaces(username, vpath); previous != nil {
		m.detachFaces(username, previous)
	}
	m.options.Database.Delete(facesTable, photoKey(username, vpath))
}

// detachFaces decrements the face counter of every person referenced by the
// entry, deleting people that no longer have any face.
func (m *Manager) detachFaces(username string, entry *PhotoFaces) {
	for _, face := range entry.Faces {
		key := personKey(username, face.PersonID)
		if !m.options.Database.KeyExists(peopleTable, key) {
			continue
		}
		person := Person{}
		if err := m.options.Database.Read(peopleTable, key, &person); err != nil {
			continue
		}
		person.FaceCount--
		if person.FaceCount <= 0 {
			m.options.Database.Delete(peopleTable, key)
		} else {
			m.options.Database.Write(peopleTable, key, person)
		}
	}
}

// assignToPerson finds the best matching person for a descriptor or creates
// a new one, updating the matched person's centroid and counter. People built
// with a different engine signature are ignored so descriptors are never
// compared across engines.
func (m *Manager) assignToPerson(username string, people []*Person, descriptor []float32, mt matcher) *Person {
	var best *Person
	bestDistance := mt.threshold
	for _, person := range people {
		if person.Signature != mt.signature {
			continue
		}
		distance := mt.distance(person.Centroid, descriptor)
		if distance <= bestDistance {
			bestDistance = distance
			best = person
		}
	}

	if best == nil {
		best = &Person{
			ID:        m.nextPersonID(username, people),
			Name:      "",
			FaceCount: 1,
			Signature: mt.signature,
			Centroid:  append([]float32{}, descriptor...),
		}
	} else {
		//Move the centroid towards the new face (running average)
		count := float32(best.FaceCount)
		for i := range best.Centroid {
			best.Centroid[i] = (best.Centroid[i]*count + descriptor[i]) / (count + 1)
		}
		best.FaceCount++
		//Deep embeddings are compared by cosine, so keep the centroid unit-length
		if mt.cosine {
			l2normalize(best.Centroid)
		}
	}

	m.options.Database.Write(peopleTable, personKey(username, best.ID), best)
	return best
}

// nextPersonID returns the next unused numeric person id of a user
func (m *Manager) nextPersonID(username string, people []*Person) string {
	highest := 0
	for _, person := range people {
		if id, err := strconv.Atoi(person.ID); err == nil && id > highest {
			highest = id
		}
	}
	return strconv.Itoa(highest + 1)
}

// ListPeople returns every person of a user, biggest cluster first
func (m *Manager) ListPeople(username string) []*Person {
	people := []*Person{}
	entries, err := m.options.Database.ListTable(peopleTable)
	if err != nil {
		return people
	}
	prefix := username + "/"
	for _, entry := range entries {
		if !strings.HasPrefix(string(entry[0]), prefix) {
			continue
		}
		person := Person{}
		if err := json.Unmarshal(entry[1], &person); err != nil {
			continue
		}
		people = append(people, &person)
	}
	sort.Slice(people, func(i, j int) bool {
		if people[i].FaceCount != people[j].FaceCount {
			return people[i].FaceCount > people[j].FaceCount
		}
		return people[i].ID < people[j].ID
	})
	return people
}

// GetPerson returns one person of a user, or nil when it does not exist
func (m *Manager) GetPerson(username string, personID string) *Person {
	key := personKey(username, personID)
	if !m.options.Database.KeyExists(peopleTable, key) {
		return nil
	}
	person := Person{}
	if err := m.options.Database.Read(peopleTable, key, &person); err != nil {
		return nil
	}
	return &person
}

// RenamePerson sets the display name of a person. An empty name resets it
// to the auto generated label.
func (m *Manager) RenamePerson(username string, personID string, name string) error {
	person := m.GetPerson(username, personID)
	if person == nil {
		return errPersonNotFound
	}
	name = strings.TrimSpace(name)
	if len(name) > 64 {
		name = name[:64]
	}
	person.Name = name
	return m.options.Database.Write(peopleTable, personKey(username, personID), person)
}

// ListPersonPhotos returns the photos of a user containing a given person
func (m *Manager) ListPersonPhotos(username string, personID string) []*PhotoFaces {
	results := []*PhotoFaces{}
	entries, err := m.options.Database.ListTable(facesTable)
	if err != nil {
		return results
	}
	prefix := username + "/"
	for _, entry := range entries {
		if !strings.HasPrefix(string(entry[0]), prefix) {
			continue
		}
		photo := PhotoFaces{}
		if err := json.Unmarshal(entry[1], &photo); err != nil {
			continue
		}
		for _, face := range photo.Faces {
			if face.PersonID == personID {
				results = append(results, &photo)
				break
			}
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].ModTime > results[j].ModTime
	})
	return results
}

// UserStats summarises the stored face data of one user
type UserStats struct {
	ScannedPhotos int `json:"scannedPhotos"`
	TotalFaces    int `json:"totalFaces"`
	People        int `json:"people"`
}

// GetUserStats counts the stored face data of a user
func (m *Manager) GetUserStats(username string) UserStats {
	stats := UserStats{}
	prefix := username + "/"
	if entries, err := m.options.Database.ListTable(facesTable); err == nil {
		for _, entry := range entries {
			if !strings.HasPrefix(string(entry[0]), prefix) {
				continue
			}
			photo := PhotoFaces{}
			if err := json.Unmarshal(entry[1], &photo); err != nil {
				continue
			}
			stats.ScannedPhotos++
			stats.TotalFaces += len(photo.Faces)
		}
	}
	if entries, err := m.options.Database.ListTable(peopleTable); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(string(entry[0]), prefix) {
				stats.People++
			}
		}
	}
	return stats
}

// ClearUserData removes every stored face and person of one user
func (m *Manager) ClearUserData(username string) error {
	prefix := username + "/"
	for _, table := range []string{facesTable, peopleTable} {
		entries, err := m.options.Database.ListTable(table)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(string(entry[0]), prefix) {
				m.options.Database.Delete(table, string(entry[0]))
			}
		}
	}
	return nil
}

// ClearAllData wipes the face database of every user. Used by the admin
// "clear all face data" action in System Settings.
func (m *Manager) ClearAllData() error {
	for _, table := range []string{facesTable, peopleTable} {
		if err := m.options.Database.DropTable(table); err != nil {
			return err
		}
		if err := m.options.Database.NewTable(table); err != nil {
			return err
		}
	}
	return nil
}

// DisplayName returns the user visible name of a person
func (p *Person) DisplayName() string {
	if p.Name != "" {
		return p.Name
	}
	return "Person " + p.ID
}

// encodeThumb encodes a face crop as a base64 JPEG data string
func encodeThumb(img image.Image) string {
	if img == nil {
		return ""
	}
	buffer := bytes.Buffer{}
	if err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 80}); err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}
