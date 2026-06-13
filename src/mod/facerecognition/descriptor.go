package facerecognition

/*
	descriptor.go

	Lightweight face descriptor used to group detected faces into people.

	The descriptor has two parts, concatenated into one vector:

	  1. LBPH (Local Binary Pattern Histogram) — texture / structure.
	     The face crop is normalized to a fixed-size grayscale patch, divided
	     into a grid of cells, and a uniform LBP(8,1) histogram (59 bins) is
	     computed per cell. LBP is intentionally illumination-invariant, which
	     makes it robust to lighting but means it discards skin tone entirely.

	  2. Tone — per-cell mean luminance (one value per cell). This restores
	     the brightness / skin-tone information that LBP throws away, so two
	     people with clearly different skin tones are not merged just because
	     their facial texture happens to be similar.

	Faces are compared with DescriptorDistance, which blends an averaged
	chi-square distance over the LBP part with the mean luminance difference
	over the central (face) cells. Lower is more similar; 0 is identical.

	This is intentionally a small classical model: it runs everywhere the
	binary runs, needs no external model files and keeps all biometric data
	on the host. Grouping quality is approximate compared to deep-learning
	embeddings, which is documented in the settings UI.
*/

import (
	"image"
	"math"
)

const (
	descriptorFaceSize = 96 //Face crops are normalized to this square size
	descriptorGrid     = 6  //Grid of descriptorGrid x descriptorGrid cells
	descriptorBins     = 59 //Uniform LBP(8,1) has 58 uniform patterns + 1 catch-all bin

	//Size of the square face thumbnail stored for the People UI
	faceThumbSize = 96

	//descriptorVersion is bumped whenever the descriptor layout or meaning
	//changes. Stored face data computed with a different version is cleared
	//and re-scanned automatically (see Manager.migrateDescriptorVersion).
	descriptorVersion = 2

	//Relative weights of the two descriptor parts when comparing faces.
	//Tone is weighted strongly because it is the cue that separates people
	//of different skin tone, which the LBP part cannot see.
	lbpDistanceWeight  = 0.35
	toneDistanceWeight = 2.2
)

const (
	//lbpDescriptorLength is the size of the LBP (texture) section
	lbpDescriptorLength = descriptorGrid * descriptorGrid * descriptorBins
	//toneDescriptorLength is the size of the tone (per-cell mean luma) section
	toneDescriptorLength = descriptorGrid * descriptorGrid
	//DescriptorLength is the total number of float values in one descriptor
	DescriptorLength = lbpDescriptorLength + toneDescriptorLength
)

// uniformLBPTable maps each of the 256 LBP codes to its bin index:
// uniform patterns (at most two 0-1 transitions) get their own bin,
// everything else shares the last bin.
var uniformLBPTable = buildUniformLBPTable()

func buildUniformLBPTable() [256]int {
	var table [256]int
	next := 0
	for code := 0; code < 256; code++ {
		if lbpTransitions(uint8(code)) <= 2 {
			table[code] = next
			next++
		} else {
			table[code] = descriptorBins - 1
		}
	}
	return table
}

// lbpTransitions counts the 0-1 and 1-0 transitions in the circular bit
// pattern of an LBP code
func lbpTransitions(code uint8) int {
	transitions := 0
	for i := 0; i < 8; i++ {
		current := (code >> uint(i)) & 1
		following := (code >> uint((i+1)%8)) & 1
		if current != following {
			transitions++
		}
	}
	return transitions
}

// ComputeDescriptor builds the combined LBPH + tone descriptor of a face crop
func ComputeDescriptor(face image.Image) []float32 {
	pixels := grayscalePixels(face, descriptorFaceSize, descriptorFaceSize)
	descriptor := make([]float32, DescriptorLength)
	computeLBPInto(descriptor[:lbpDescriptorLength], pixels, descriptorFaceSize, descriptorFaceSize)
	computeToneInto(descriptor[lbpDescriptorLength:], pixels, descriptorFaceSize, descriptorFaceSize)
	return descriptor
}

// computeLBPInto runs uniform LBP(8,1) over a grayscale patch and writes the
// concatenated per-cell histograms (each L1-normalized) into dst.
func computeLBPInto(dst []float32, pixels []uint8, w int, h int) {
	cellW := w / descriptorGrid
	cellH := h / descriptorGrid

	for cy := 0; cy < descriptorGrid; cy++ {
		for cx := 0; cx < descriptorGrid; cx++ {
			histogram := dst[(cy*descriptorGrid+cx)*descriptorBins:][:descriptorBins]
			count := 0
			for y := cy * cellH; y < (cy+1)*cellH; y++ {
				for x := cx * cellW; x < (cx+1)*cellW; x++ {
					if x < 1 || y < 1 || x >= w-1 || y >= h-1 {
						continue //LBP needs a full 3x3 neighbourhood
					}
					histogram[uniformLBPTable[lbpCode(pixels, w, x, y)]]++
					count++
				}
			}
			if count > 0 {
				for i := range histogram {
					histogram[i] /= float32(count)
				}
			}
		}
	}
}

// computeToneInto writes the per-cell mean luminance (normalized to 0..1) of a
// grayscale patch into dst, one value per grid cell.
func computeToneInto(dst []float32, pixels []uint8, w int, h int) {
	cellW := w / descriptorGrid
	cellH := h / descriptorGrid

	for cy := 0; cy < descriptorGrid; cy++ {
		for cx := 0; cx < descriptorGrid; cx++ {
			sum := 0.0
			count := 0
			for y := cy * cellH; y < (cy+1)*cellH; y++ {
				for x := cx * cellW; x < (cx+1)*cellW; x++ {
					sum += float64(pixels[y*w+x])
					count++
				}
			}
			mean := float32(0)
			if count > 0 {
				mean = float32(sum / float64(count) / 255.0)
			}
			dst[cy*descriptorGrid+cx] = mean
		}
	}
}

// lbpCode computes the 8-neighbour LBP code of pixel (x,y)
func lbpCode(pixels []uint8, w int, x int, y int) uint8 {
	center := pixels[y*w+x]
	var code uint8
	neighbours := [8][2]int{
		{-1, -1}, {0, -1}, {1, -1},
		{1, 0},
		{1, 1}, {0, 1}, {-1, 1},
		{-1, 0},
	}
	for i, offset := range neighbours {
		if pixels[(y+offset[1])*w+(x+offset[0])] >= center {
			code |= 1 << uint(i)
		}
	}
	return code
}

// DescriptorDistance compares two descriptors by blending the averaged
// chi-square distance of their LBP (texture) sections with the mean
// luminance difference of their central (face) cells. Returns 0 for
// identical descriptors and math.MaxFloat64 when the descriptors are not
// comparable (wrong length).
func DescriptorDistance(a []float32, b []float32) float64 {
	if len(a) != len(b) || len(a) != DescriptorLength {
		return math.MaxFloat64
	}

	//LBP: averaged chi-square over the texture histograms
	const epsilon = 1e-7
	chiSquare := 0.0
	for i := 0; i < lbpDescriptorLength; i++ {
		diff := float64(a[i] - b[i])
		total := float64(a[i] + b[i])
		if total > epsilon {
			chiSquare += diff * diff / total
		}
	}
	lbpDistance := chiSquare / float64(descriptorGrid*descriptorGrid)

	//Tone: mean absolute luminance difference over the central cells, where
	//the face (rather than the background border) dominates. This is the
	//part that separates different skin tones.
	toneSum := 0.0
	toneCount := 0
	for cy := 1; cy < descriptorGrid-1; cy++ {
		for cx := 1; cx < descriptorGrid-1; cx++ {
			idx := lbpDescriptorLength + cy*descriptorGrid + cx
			toneSum += math.Abs(float64(a[idx] - b[idx]))
			toneCount++
		}
	}
	toneDistance := 0.0
	if toneCount > 0 {
		toneDistance = toneSum / float64(toneCount)
	}

	return lbpDistanceWeight*lbpDistance + toneDistanceWeight*toneDistance
}
