package facerecognition

/*
	detector.go

	Face detection built on the pure Go pigo cascade classifier. The
	classifier data (the "facefinder" cascade from the upstream pigo
	project, MIT licensed) is embedded into the binary so the feature works
	on every supported platform without downloading models or shelling out
	to system tools.
*/

import (
	"bytes"
	_ "embed"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"sync"

	pigo "github.com/esimov/pigo/core"
	"github.com/nfnt/resize"
	"golang.org/x/image/webp"
)

//go:embed cascade/facefinder
var cascadeData []byte

const (
	//Images are downscaled to this max dimension before detection. Keeps the
	//cascade fast on large photos while remaining accurate enough for albums.
	detectionMaxDimension = 1200

	//Minimum detection quality (pigo Q score) accepted as a face
	detectionMinQuality = 5.0

	//IoU threshold used to merge overlapping raw detections
	detectionClusterIoU = 0.18
)

// DetectedFace is a single face found in a photo. Coordinates are in pixels
// relative to the original (non-downscaled) image.
type DetectedFace struct {
	X          int     `json:"x"`
	Y          int     `json:"y"`
	W          int     `json:"w"`
	H          int     `json:"h"`
	Quality    float64 `json:"quality"`
	descriptor []float32
	thumb      image.Image
}

type detector struct {
	classifier *pigo.Pigo
	unpackOnce sync.Once
	unpackErr  error
}

func newDetector() *detector {
	return &detector{}
}

// classifierInstance lazily unpacks the embedded cascade
func (d *detector) classifierInstance() (*pigo.Pigo, error) {
	d.unpackOnce.Do(func() {
		d.classifier, d.unpackErr = pigo.NewPigo().Unpack(cascadeData)
	})
	return d.classifier, d.unpackErr
}

// DecodeImage decodes a photo from raw bytes. The format is derived from the
// content itself; ext is only used to pick the webp decoder which is not
// registered in the standard image package.
func DecodeImage(imageBytes []byte, ext string) (image.Image, error) {
	if strings.TrimPrefix(strings.ToLower(ext), ".") == "webp" {
		return webp.Decode(bytes.NewReader(imageBytes))
	}
	img, _, err := image.Decode(bytes.NewReader(imageBytes))
	return img, err
}

// DetectFaces finds the faces inside img. minFaceSize is the smallest
// accepted face in pixels of the detection-scaled image. The returned faces
// carry their grouping descriptor and a small thumbnail crop, both computed
// from the same decoded image.
func (d *detector) DetectFaces(img image.Image, minFaceSize int) ([]*DetectedFace, error) {
	classifier, err := d.classifierInstance()
	if err != nil {
		return nil, errors.New("unable to load face detection cascade: " + err.Error())
	}

	bounds := img.Bounds()
	if bounds.Dx() < 2 || bounds.Dy() < 2 {
		return []*DetectedFace{}, nil
	}

	//Downscale large images for detection speed; remember the scale so the
	//face rectangles can be mapped back onto the original image.
	scaled, scale := downscaleForDetection(img)
	nrgba := pigo.ImgToNRGBA(scaled)
	pixels := pigo.RgbToGrayscale(nrgba)

	cols := nrgba.Bounds().Dx()
	rows := nrgba.Bounds().Dy()
	maxSize := cols
	if rows > maxSize {
		maxSize = rows
	}
	if minFaceSize < 20 {
		minFaceSize = 20
	}
	if minFaceSize >= maxSize {
		return []*DetectedFace{}, nil
	}

	params := pigo.CascadeParams{
		MinSize:     minFaceSize,
		MaxSize:     maxSize,
		ShiftFactor: 0.1,
		ScaleFactor: 1.1,
		ImageParams: pigo.ImageParams{
			Pixels: pixels,
			Rows:   rows,
			Cols:   cols,
			Dim:    cols,
		},
	}

	detections := classifier.RunCascade(params, 0.0)
	detections = classifier.ClusterDetections(detections, detectionClusterIoU)

	faces := []*DetectedFace{}
	for _, det := range detections {
		if float64(det.Q) < detectionMinQuality {
			continue
		}

		//pigo reports the face center and its size; convert to a rectangle
		//clamped to the scaled image, then crop for descriptor + thumbnail.
		half := det.Scale / 2
		rect := image.Rect(det.Col-half, det.Row-half, det.Col+half, det.Row+half)
		rect = rect.Intersect(nrgba.Bounds())
		if rect.Dx() < 8 || rect.Dy() < 8 {
			continue
		}

		crop := cropImage(nrgba, rect)
		face := &DetectedFace{
			X:          int(float64(rect.Min.X) * scale),
			Y:          int(float64(rect.Min.Y) * scale),
			W:          int(float64(rect.Dx()) * scale),
			H:          int(float64(rect.Dy()) * scale),
			Quality:    float64(det.Q),
			descriptor: ComputeDescriptor(crop),
			thumb:      resize.Resize(faceThumbSize, faceThumbSize, crop, resize.Lanczos3),
		}
		faces = append(faces, face)
		if len(faces) >= maxFacesPerPhoto {
			break
		}
	}

	return faces, nil
}

// downscaleForDetection resizes img so its longest side is at most
// detectionMaxDimension. It returns the (possibly original) image and the
// factor that maps scaled coordinates back to original ones.
func downscaleForDetection(img image.Image) (image.Image, float64) {
	bounds := img.Bounds()
	longest := bounds.Dx()
	if bounds.Dy() > longest {
		longest = bounds.Dy()
	}
	if longest <= detectionMaxDimension {
		return img, 1.0
	}

	scale := float64(longest) / float64(detectionMaxDimension)
	if bounds.Dx() >= bounds.Dy() {
		return resize.Resize(detectionMaxDimension, 0, img, resize.Bilinear), scale
	}
	return resize.Resize(0, detectionMaxDimension, img, resize.Bilinear), scale
}

// cropImage copies the given region of src into a stand-alone image
func cropImage(src *image.NRGBA, rect image.Rectangle) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	for y := 0; y < rect.Dy(); y++ {
		for x := 0; x < rect.Dx(); x++ {
			out.Set(x, y, src.At(rect.Min.X+x, rect.Min.Y+y))
		}
	}
	return out
}

// grayscalePixels converts an image into a w*h slice of 8-bit luma values
func grayscalePixels(img image.Image, w int, h int) []uint8 {
	scaled := resize.Resize(uint(w), uint(h), img, resize.Bilinear)
	pixels := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.GrayModel.Convert(scaled.At(scaled.Bounds().Min.X+x, scaled.Bounds().Min.Y+y)).(color.Gray)
			pixels[y*w+x] = c.Y
		}
	}
	return pixels
}
