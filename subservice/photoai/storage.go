package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/google/uuid"
)

// FaceCluster is a group of face detections believed to belong to the same person.
type FaceCluster struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Count     int    `json:"count"`
	CreatedAt int64  `json:"created_at"`
}

// FaceDetectionRecord is a stored face detection linked to a photo.
type FaceDetectionRecord struct {
	ID         string  `json:"id"`
	ClusterID  string  `json:"cluster_id"`
	PersonName string  `json:"person_name"`
	PhotoVpath string  `json:"photo_vpath"`
	BBoxX      float64 `json:"bbox_x"`
	BBoxY      float64 `json:"bbox_y"`
	BBoxW      float64 `json:"bbox_w"`
	BBoxH      float64 `json:"bbox_h"`
	Confidence float64 `json:"confidence"`
}

// FaceStorage manages per-user face clusters in a local SQLite database.
type FaceStorage struct {
	db *sql.DB
}

// 0.45 is appropriate for L2-normalised 512-dim MobileFaceNet embeddings;
// the colour-grid fallback uses a higher effective threshold due to lower dimensionality.
const clusterSimilarityThreshold = 0.45

func openFaceStorage(dataDir string) (*FaceStorage, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "faces.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	if err := initFaceSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &FaceStorage{db: db}, nil
}

func initFaceSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS face_clusters (
			id         TEXT PRIMARY KEY,
			username   TEXT NOT NULL,
			name       TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS face_detections (
			id          TEXT PRIMARY KEY,
			cluster_id  TEXT NOT NULL,
			username    TEXT NOT NULL,
			photo_vpath TEXT NOT NULL,
			bbox_x      REAL,
			bbox_y      REAL,
			bbox_w      REAL,
			bbox_h      REAL,
			confidence  REAL,
			feature     TEXT,
			created_at  INTEGER NOT NULL,
			UNIQUE(username, photo_vpath, bbox_x, bbox_y)
		);

		CREATE INDEX IF NOT EXISTS idx_fd_cluster ON face_detections(cluster_id);
		CREATE INDEX IF NOT EXISTS idx_fd_user    ON face_detections(username);
		CREATE INDEX IF NOT EXISTS idx_fd_photo   ON face_detections(username, photo_vpath);
		CREATE INDEX IF NOT EXISTS idx_fc_user    ON face_clusters(username);
	`)
	return err
}

func (s *FaceStorage) Close() {
	if s.db != nil {
		s.db.Close()
	}
}

// ListClusters returns all face clusters for a user with detection counts.
func (s *FaceStorage) ListClusters(username string) ([]FaceCluster, error) {
	rows, err := s.db.Query(`
		SELECT fc.id, fc.name, fc.created_at, COUNT(fd.id) AS cnt
		FROM face_clusters fc
		LEFT JOIN face_detections fd ON fd.cluster_id = fc.id AND fd.username = fc.username
		WHERE fc.username = ?
		GROUP BY fc.id
		ORDER BY cnt DESC, fc.created_at DESC
	`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clusters []FaceCluster
	for rows.Next() {
		var c FaceCluster
		if err := rows.Scan(&c.ID, &c.Name, &c.CreatedAt, &c.Count); err != nil {
			continue
		}
		clusters = append(clusters, c)
	}
	return clusters, rows.Err()
}

// LabelCluster sets the display name for a cluster.
func (s *FaceStorage) LabelCluster(username, clusterID, name string) error {
	_, err := s.db.Exec(
		`UPDATE face_clusters SET name = ? WHERE id = ? AND username = ?`,
		name, clusterID, username,
	)
	return err
}

// MergeClusters moves all detections from srcID into dstID and deletes srcID.
func (s *FaceStorage) MergeClusters(username, srcID, dstID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.Exec(
		`UPDATE face_detections SET cluster_id = ? WHERE cluster_id = ? AND username = ?`,
		dstID, srcID, username,
	); err != nil {
		return err
	}
	if _, err = tx.Exec(
		`DELETE FROM face_clusters WHERE id = ? AND username = ?`,
		srcID, username,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteCluster removes a cluster and all its detections.
func (s *FaceStorage) DeleteCluster(username, clusterID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM face_detections WHERE cluster_id = ? AND username = ?`, clusterID, username); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM face_clusters WHERE id = ? AND username = ?`, clusterID, username); err != nil {
		return err
	}
	return tx.Commit()
}

// GetFacesInPhoto returns all face detections (with cluster names) for a photo.
func (s *FaceStorage) GetFacesInPhoto(username, photoVpath string) ([]FaceDetectionRecord, error) {
	rows, err := s.db.Query(`
		SELECT fd.id, fd.cluster_id, COALESCE(fc.name,''),
		       fd.photo_vpath, fd.bbox_x, fd.bbox_y, fd.bbox_w, fd.bbox_h, fd.confidence
		FROM face_detections fd
		JOIN face_clusters fc ON fc.id = fd.cluster_id
		WHERE fd.username = ? AND fd.photo_vpath = ?
	`, username, photoVpath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []FaceDetectionRecord
	for rows.Next() {
		var r FaceDetectionRecord
		if err := rows.Scan(&r.ID, &r.ClusterID, &r.PersonName, &r.PhotoVpath,
			&r.BBoxX, &r.BBoxY, &r.BBoxW, &r.BBoxH, &r.Confidence); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SaveDetections stores face detections for a photo, assigning each to the
// closest existing cluster or creating a new cluster when none matches.
// Returns the cluster IDs parallel to dets.
func (s *FaceStorage) SaveDetections(username, photoVpath string, dets []FaceDetection) ([]string, error) {
	clusterIDs := make([]string, len(dets))
	now := time.Now().Unix()

	for i, det := range dets {
		clusterID, err := s.findOrCreateCluster(username, det.Feature, now)
		if err != nil {
			fmt.Fprintf(os.Stderr, "photoai: cluster lookup error: %v\n", err)
			continue
		}
		clusterIDs[i] = clusterID

		featureJSON, _ := json.Marshal(det.Feature)
		id := uuid.New().String()
		if _, err = s.db.Exec(`
			INSERT OR IGNORE INTO face_detections
			    (id, cluster_id, username, photo_vpath, bbox_x, bbox_y, bbox_w, bbox_h, confidence, feature, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)
		`, id, clusterID, username, photoVpath,
			det.BBoxX, det.BBoxY, det.BBoxW, det.BBoxH, det.Confidence,
			string(featureJSON), now); err != nil {
			fmt.Fprintf(os.Stderr, "photoai: insert detection error: %v\n", err)
		}
	}
	return clusterIDs, nil
}

// findOrCreateCluster returns the cluster ID for the best-matching existing
// cluster, or inserts a new cluster when none exceeds the similarity threshold.
func (s *FaceStorage) findOrCreateCluster(username string, feature []float64, now int64) (string, error) {
	if len(feature) == 0 {
		return s.newCluster(username, now)
	}

	rows, err := s.db.Query(`
		SELECT fc.id, fd.feature
		FROM face_clusters fc
		JOIN face_detections fd ON fd.cluster_id = fc.id
		WHERE fc.username = ?
	`, username)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	bestID := ""
	bestSim := -1.0
	for rows.Next() {
		var clusterID, featureStr string
		if err := rows.Scan(&clusterID, &featureStr); err != nil {
			continue
		}
		var stored []float64
		if err := json.Unmarshal([]byte(featureStr), &stored); err != nil {
			continue
		}
		if sim := cosineSimilarity(feature, stored); sim > bestSim {
			bestSim = sim
			bestID = clusterID
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	if bestSim >= clusterSimilarityThreshold {
		return bestID, nil
	}
	return s.newCluster(username, now)
}

func (s *FaceStorage) newCluster(username string, now int64) (string, error) {
	id := uuid.New().String()
	_, err := s.db.Exec(
		`INSERT INTO face_clusters (id, username, name, created_at) VALUES (?,?,?,?)`,
		id, username, "", now,
	)
	return id, err
}

func cosineSimilarity(a, b []float64) float64 {
	n := len(a)
	if n == 0 || len(b) != n {
		return 0
	}
	var dot, normA, normB float64
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
