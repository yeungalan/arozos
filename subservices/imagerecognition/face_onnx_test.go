//go:build onnx

package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"math"
	"testing"
)

/*
	face_onnx_test.go  (build tag: onnx)

	End-to-end validation of the DNN face pipeline (YuNet detection + SFace
	recognition). Runs only when the ONNX models + runtime are available
	(models/model.json, the .onnx files and libonnxruntime); otherwise it skips,
	so `go test -tags onnx` still passes on machines without the models.
*/

func newONNXRecognizer(t *testing.T) *Recognizer {
	t.Helper()
	r, err := NewRecognizer(t.TempDir(), newSvcLogger("[onnxtest]"))
	if err != nil {
		t.Fatalf("NewRecognizer: %v", err)
	}
	if r.faceEmbedderName() != "onnx-sface" || r.faceDetectorName() != "onnx-yunet" {
		r.Close()
		t.Skip("ONNX face models/runtime not available; skipping DNN face test")
	}
	t.Cleanup(r.Close)
	return r
}

// rotateImage rotates img about its centre by deg degrees (bilinear).
func rotateImage(img image.Image, deg float64) image.Image {
	rad := deg * math.Pi / 180
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	cx, cy := float64(w)/2, float64(h)/2
	cos, sin := math.Cos(rad), math.Sin(rad)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for oy := 0; oy < h; oy++ {
		for ox := 0; ox < w; ox++ {
			dx, dy := float64(ox)-cx, float64(oy)-cy
			sx := cx + dx*cos + dy*sin
			sy := cy - dx*sin + dy*cos
			out.Set(ox, oy, bilinearSample(img, sx, sy))
		}
	}
	return out
}

// jpegRoundTrip re-encodes img as JPEG to mimic a different camera/compression.
func jpegRoundTrip(t *testing.T, img image.Image, quality int) image.Image {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
	out, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestONNXFaceDetectionAccuracy(t *testing.T) {
	r := newONNXRecognizer(t)
	for _, name := range []string{"person_a.jpg", "person_b.jpg", "person_c.jpg"} {
		faces := r.DetectFaces(loadFixture(t, name))
		if len(faces) != 1 {
			t.Errorf("%s: detected %d faces, want exactly 1", name, len(faces))
			continue
		}
		f := faces[0]
		if f.Confidence < 0.7 {
			t.Errorf("%s: low detector confidence %.3f", name, f.Confidence)
		}
		if len(f.Landmarks) != 5 {
			t.Errorf("%s: got %d landmarks, want 5", name, len(f.Landmarks))
		}
	}

	//A non-face scene must yield no faces (the failure mode the builtin cascade
	//suffered from).
	scene := makeScene(400, 300)
	if faces := r.DetectFaces(scene); len(faces) != 0 {
		t.Errorf("non-face scene produced %d false faces", len(faces))
	}
}

func TestONNXGroupsSamePersonAcrossPhotos(t *testing.T) {
	r := newONNXRecognizer(t)
	personA := loadFixture(t, "person_a.jpg")

	uuidA := r.RecognizeFaces(personA)[0].PersonUUID
	if uuidA == "" {
		t.Fatal("no UUID assigned")
	}

	//Simulate different photos of the SAME person via realistic augmentations.
	variants := map[string]image.Image{
		"rotated +8deg":     rotateImage(personA, 8),
		"rotated -8deg":     rotateImage(personA, -8),
		"brighter":          adjustBrightness(personA, 1.25),
		"darker":            adjustBrightness(personA, 0.8),
		"scaled+recompress": jpegRoundTrip(t, resizeImage(personA, 240, 300), 80),
	}
	for label, v := range variants {
		faces := r.RecognizeFaces(v)
		if len(faces) == 0 {
			t.Errorf("%s: no face detected", label)
			continue
		}
		if faces[0].PersonUUID != uuidA {
			t.Errorf("%s: grouped as %s (score %.3f), want same person %s",
				label, faces[0].PersonUUID[:8], faces[0].MatchScore, uuidA[:8])
		}
	}

	//Different people must remain distinct.
	uuidB := r.RecognizeFaces(loadFixture(t, "person_b.jpg"))[0].PersonUUID
	uuidC := r.RecognizeFaces(loadFixture(t, "person_c.jpg"))[0].PersonUUID
	if uuidB == uuidA || uuidC == uuidA || uuidB == uuidC {
		t.Errorf("distinct people merged: A=%s B=%s C=%s", uuidA[:8], uuidB[:8], uuidC[:8])
	}

	//Exactly three identities should exist (A with its variants, B, C).
	if got := r.people.Count(); got != 3 {
		t.Errorf("known people = %d, want 3", got)
	}
}

// makeScene builds a non-face test image (sky gradient over textured ground).
func makeScene(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var c colorRGBA
			if y < h/2 {
				c = colorRGBA{R: 135, G: 206, B: 235, A: 255} //sky
			} else {
				n := uint8((x*7 + y*13) % 40)
				c = colorRGBA{R: 70 + n, G: 110 + n, B: 60 + n, A: 255} //ground texture
			}
			img.Set(x, y, c)
		}
	}
	return img
}
