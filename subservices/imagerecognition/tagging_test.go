package main

import (
	"image"
	"image/color"
	"strings"
	"testing"
)

func hasTag(tags []Tag, label string) bool {
	for _, t := range tags {
		if t.Label == label {
			return true
		}
	}
	return false
}

func TestSceneTagsBrightnessAndColor(t *testing.T) {
	tests := []struct {
		name      string
		img       image.Image
		wantTag   string
		absentTag string
	}{
		{"white is bright + black-and-white", solidImage(50, 50, color.White), "bright", "dark"},
		{"black is dark", solidImage(50, 50, color.Black), "dark", "bright"},
		{"saturated red is colorful", solidImage(50, 50, color.RGBA{220, 10, 10, 255}), "colorful", "black-and-white"},
		{"grey is black-and-white", solidImage(50, 50, color.RGBA{128, 128, 128, 255}), "black-and-white", "colorful"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tags := sceneTags(tc.img)
			if !hasTag(tags, tc.wantTag) {
				t.Errorf("missing expected tag %q in %+v", tc.wantTag, tags)
			}
			if tc.absentTag != "" && hasTag(tags, tc.absentTag) {
				t.Errorf("unexpected tag %q present in %+v", tc.absentTag, tags)
			}
		})
	}
}

func TestSceneTagsOrientation(t *testing.T) {
	tests := []struct {
		name string
		w, h int
		want string
	}{
		{"wide is landscape", 200, 80, "landscape"},
		{"tall is portrait", 80, 200, "portrait"},
		{"equal is square", 100, 100, "square"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			img := solidImage(tc.w, tc.h, color.RGBA{200, 100, 50, 255})
			if tags := sceneTags(img); !hasTag(tags, tc.want) {
				t.Errorf("orientation: missing %q in %+v", tc.want, tags)
			}
		})
	}
}

func TestSceneTagsDominantHue(t *testing.T) {
	//A saturated blue image should produce a blue-family hue tag (the exact
	//sector may be "blue-tones" or the adjacent "sky-blue-tones").
	img := solidImage(60, 60, color.RGBA{20, 40, 220, 255})
	tags := sceneTags(img)
	hasBlueFamily := false
	for _, tag := range tags {
		if tag.Source == "color" && strings.Contains(tag.Label, "blue") {
			hasBlueFamily = true
		}
	}
	if !hasBlueFamily {
		t.Errorf("expected a blue-family hue tag in %+v", tags)
	}
}

func TestAttributeTags(t *testing.T) {
	green := solidImage(80, 80, color.RGBA{40, 150, 50, 255})
	if tags := attributeTags(green); !hasTag(tags, "nature") || !hasTag(tags, "outdoor") {
		t.Errorf("green image: expected nature+outdoor in %+v", tags)
	}
	dark := solidImage(80, 80, color.RGBA{10, 10, 12, 255})
	if tags := attributeTags(dark); !hasTag(tags, "night") {
		t.Errorf("dark image: expected night in %+v", tags)
	}
	//Confidences must stay below 1.0.
	for _, tag := range attributeTags(green) {
		if tag.Confidence > 1.0 {
			t.Errorf("confidence %v exceeds 1.0 for %q", tag.Confidence, tag.Label)
		}
	}
}

func TestCompositionTags(t *testing.T) {
	tests := []struct {
		name  string
		faces []Face
		want  string
	}{
		{"single face is portrait", []Face{{Box: Box{0, 0, 10, 10}}}, "portrait"},
		{"two faces", []Face{{Box: Box{0, 0, 10, 10}}, {Box: Box{20, 0, 10, 10}}}, "two people"},
		{"four faces is a group", []Face{{}, {}, {}, {}}, "group photo"},
		{"many faces is a crowd", make([]Face, 8), "crowd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := compositionTags(tc.faces, 100, 100); !hasTag(got, tc.want) {
				t.Errorf("got %+v, want tag %q", got, tc.want)
			}
		})
	}

	//A face filling much of the frame is a close-up.
	big := []Face{{Box: Box{0, 0, 60, 60}}}
	tags := compositionTags(big, 100, 100)
	if !hasTag(tags, "close-up") {
		t.Errorf("large face: expected close-up in %+v", tags)
	}
	for _, tag := range tags {
		if tag.Confidence > 1.0 {
			t.Errorf("confidence %v exceeds 1.0", tag.Confidence)
		}
	}
}

func TestDedupeTags(t *testing.T) {
	in := []Tag{
		{Label: "person", Confidence: 0.6},
		{Label: "person", Confidence: 0.9}, //higher confidence wins
		{Label: "dog", Confidence: 0.8},
	}
	out := dedupeTags(in)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(out), out)
	}
	//Sorted by confidence descending -> person(0.9) first.
	if out[0].Label != "person" || out[0].Confidence != 0.9 {
		t.Errorf("first tag = %+v, want person@0.9", out[0])
	}
}
