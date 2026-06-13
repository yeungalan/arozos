package facerecognition

/*
	descriptor.go

	Lightweight face descriptor used to group detected faces into people.

	The descriptor is a classic LBPH (Local Binary Pattern Histogram): the
	face crop is normalized to a fixed size grayscale patch, divided into a
	grid of cells, and a uniform LBP(8,1) histogram (59 bins) is computed
	per cell. The concatenated, per-cell normalized histograms form the
	descriptor. Faces are compared with an averaged chi-square distance, so
	0 means identical texture and larger values mean less similar faces.

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
	descriptorFaceSize = 96 //Face crops are normalized to this square size before LBP
	descriptorGrid     = 6  //Grid of descriptorGrid x descriptorGrid cells
	descriptorBins     = 59 //Uniform LBP(8,1) has 58 uniform patterns + 1 catch-all bin

	//Size of the square face thumbnail stored for the People UI
	faceThumbSize = 96
)

// DescriptorLength is the number of float values in one face descriptor
const DescriptorLength = descriptorGrid * descriptorGrid * descriptorBins

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

// ComputeDescriptor builds the LBPH descriptor of a face crop
func ComputeDescriptor(face image.Image) []float32 {
	pixels := grayscalePixels(face, descriptorFaceSize, descriptorFaceSize)
	return computeDescriptorFromGray(pixels, descriptorFaceSize, descriptorFaceSize)
}

// computeDescriptorFromGray runs uniform LBP(8,1) over a grayscale patch and
// returns the concatenated per-cell histograms, each L1-normalized.
func computeDescriptorFromGray(pixels []uint8, w int, h int) []float32 {
	descriptor := make([]float32, DescriptorLength)
	cellW := w / descriptorGrid
	cellH := h / descriptorGrid

	for cy := 0; cy < descriptorGrid; cy++ {
		for cx := 0; cx < descriptorGrid; cx++ {
			histogram := descriptor[(cy*descriptorGrid+cx)*descriptorBins:][:descriptorBins]
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
	return descriptor
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

// DescriptorDistance compares two descriptors with an averaged chi-square
// distance. Returns 0 for identical descriptors; math.MaxFloat64 when the
// descriptors are not comparable (e.g. different lengths).
func DescriptorDistance(a []float32, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return math.MaxFloat64
	}
	const epsilon = 1e-7
	sum := 0.0
	for i := range a {
		diff := float64(a[i] - b[i])
		total := float64(a[i] + b[i])
		if total > epsilon {
			sum += diff * diff / total
		}
	}
	//Average over the cells so the threshold is independent of grid size
	return sum / float64(descriptorGrid*descriptorGrid)
}
