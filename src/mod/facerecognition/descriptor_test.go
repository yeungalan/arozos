package facerecognition

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func TestLBPTransitions(t *testing.T) {
	tests := []struct {
		name string
		code uint8
		want int
	}{
		{"all zeros", 0x00, 0},
		{"all ones", 0xFF, 0},
		{"half pattern", 0x0F, 2},
		{"alternating", 0x55, 8},
		{"single bit", 0x01, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := lbpTransitions(tc.code); got != tc.want {
				t.Errorf("lbpTransitions(%#x) = %d, want %d", tc.code, got, tc.want)
			}
		})
	}
}

func TestUniformLBPTableShape(t *testing.T) {
	//Exactly 58 uniform patterns must receive their own bin, every other
	//code must share the last bin.
	uniformCount := 0
	for code := 0; code < 256; code++ {
		bin := uniformLBPTable[code]
		if bin < 0 || bin >= descriptorBins {
			t.Fatalf("code %d mapped to out-of-range bin %d", code, bin)
		}
		if lbpTransitions(uint8(code)) <= 2 {
			uniformCount++
			if bin == descriptorBins-1 {
				t.Errorf("uniform code %d mapped to the catch-all bin", code)
			}
		} else if bin != descriptorBins-1 {
			t.Errorf("non-uniform code %d mapped to bin %d, want catch-all %d", code, bin, descriptorBins-1)
		}
	}
	if uniformCount != descriptorBins-1 {
		t.Errorf("found %d uniform patterns, want %d", uniformCount, descriptorBins-1)
	}
}

func TestComputeDescriptorShape(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8((x * 4) % 256)})
		}
	}

	descriptor := ComputeDescriptor(img)
	if len(descriptor) != DescriptorLength {
		t.Fatalf("descriptor length = %d, want %d", len(descriptor), DescriptorLength)
	}

	//Every cell histogram is L1 normalized: it sums to ~1
	for cell := 0; cell < descriptorGrid*descriptorGrid; cell++ {
		sum := float64(0)
		for bin := 0; bin < descriptorBins; bin++ {
			value := float64(descriptor[cell*descriptorBins+bin])
			if value < 0 {
				t.Fatalf("cell %d bin %d is negative: %f", cell, bin, value)
			}
			sum += value
		}
		if math.Abs(sum-1.0) > 0.01 {
			t.Errorf("cell %d histogram sums to %f, want ~1.0", cell, sum)
		}
	}
}

func TestComputeDescriptorDeterministic(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 96, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8((x*7 + y*13) % 256)})
		}
	}
	first := ComputeDescriptor(img)
	second := ComputeDescriptor(img)
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("descriptor not deterministic at index %d: %f != %f", i, first[i], second[i])
		}
	}
}

// patternImage builds a 96x96 grayscale image with a fixed texture pattern
// centred on a given base brightness. Two images with the same pattern but
// different base brightness have identical LBP (LBP is brightness-invariant)
// yet different skin-tone / luminance.
func patternImage(base int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, descriptorFaceSize, descriptorFaceSize))
	for y := 0; y < descriptorFaceSize; y++ {
		for x := 0; x < descriptorFaceSize; x++ {
			//Same spatial pattern regardless of base, so the relative
			//neighbour ordering (and thus LBP) is identical.
			v := base + 30*((x/4+y/4)%2)
			if v < 0 {
				v = 0
			}
			if v > 255 {
				v = 255
			}
			img.SetGray(x, y, color.Gray{Y: uint8(v)})
		}
	}
	return img
}

// TestToneSeparatesBrightness is the regression test for the reported bug
// where two people with clearly different skin tones were grouped together.
// LBP alone cannot tell them apart; the tone part of the descriptor must.
func TestToneSeparatesBrightness(t *testing.T) {
	bright := ComputeDescriptor(patternImage(180))
	dark := ComputeDescriptor(patternImage(80))

	//Same texture, same brightness -> essentially identical
	sameDistance := DescriptorDistance(bright, ComputeDescriptor(patternImage(180)))
	if sameDistance > 0.05 {
		t.Errorf("identical faces distance = %f, want ~0", sameDistance)
	}

	//Same texture, very different brightness (skin tone) -> must be far apart,
	//well beyond the default balanced grouping threshold.
	toneDistance := DescriptorDistance(bright, dark)
	if toneDistance <= DefaultConfig().MatchThreshold {
		t.Errorf("different-tone faces distance = %f, must exceed match threshold %f",
			toneDistance, DefaultConfig().MatchThreshold)
	}
	if toneDistance <= sameDistance {
		t.Errorf("different-tone distance %f should exceed same-tone distance %f", toneDistance, sameDistance)
	}
}

func TestDescriptorDistance(t *testing.T) {
	flat := make([]float32, DescriptorLength)
	for i := range flat {
		flat[i] = 1.0 / float32(descriptorBins)
	}
	spiky := make([]float32, DescriptorLength)
	for cell := 0; cell < descriptorGrid*descriptorGrid; cell++ {
		spiky[cell*descriptorBins] = 1.0
	}

	tests := []struct {
		name string
		a    []float32
		b    []float32
		want func(distance float64) bool
	}{
		{"identical is zero", flat, flat, func(d float64) bool { return d == 0 }},
		{"different is positive", flat, spiky, func(d float64) bool { return d > 0.1 }},
		{"length mismatch is max", flat, flat[:10], func(d float64) bool { return d == math.MaxFloat64 }},
		{"empty is max", []float32{}, []float32{}, func(d float64) bool { return d == math.MaxFloat64 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescriptorDistance(tc.a, tc.b); !tc.want(got) {
				t.Errorf("DescriptorDistance() = %f, unexpected for case %s", got, tc.name)
			}
		})
	}

	//Distance must be symmetric
	if DescriptorDistance(flat, spiky) != DescriptorDistance(spiky, flat) {
		t.Errorf("DescriptorDistance is not symmetric")
	}
}
