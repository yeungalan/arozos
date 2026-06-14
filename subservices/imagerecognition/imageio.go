package main

import (
	"bytes"
	"image"
	"image/draw"

	//Register decoders for the common formats ArozOS serves.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	xdraw "golang.org/x/image/draw"
)

/*
	imageio.go

	Pure-Go image decoding and geometry helpers. No external binaries or CGO
	are required here, so the builtin recognition path runs on every platform
	ArozOS targets.
*/

// decodeImage decodes an image from raw bytes, auto-detecting the format.
func decodeImage(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// toRGBA returns an *image.RGBA copy of img (or img itself if already RGBA).
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

// cropImage returns the sub-image bounded by box, clamped to img's bounds.
func cropImage(img image.Image, box Box) image.Image {
	b := img.Bounds()
	x0 := clampInt(box.X, 0, b.Dx())
	y0 := clampInt(box.Y, 0, b.Dy())
	x1 := clampInt(box.X+box.Width, 0, b.Dx())
	y1 := clampInt(box.Y+box.Height, 0, b.Dy())
	if x1 <= x0 || y1 <= y0 {
		//Degenerate crop; return a 1x1 pixel so callers never panic.
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	rgba := toRGBA(img)
	return rgba.SubImage(image.Rect(b.Min.X+x0, b.Min.Y+y0, b.Min.X+x1, b.Min.Y+y1))
}

// resizeImage scales src to w x h using high quality interpolation.
func resizeImage(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return dst
}

// grayscalePixels returns the 8-bit grayscale pixel buffer of img in row-major
// order, together with its width and height. This is the format pigo expects.
func grayscalePixels(img image.Image) ([]uint8, int, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := make([]uint8, w*h)
	idx := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			//Rec. 601 luma; RGBA() returns 16-bit values so divide back to 8-bit.
			lum := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257.0
			pix[idx] = uint8(clampInt(int(lum+0.5), 0, 255))
			idx++
		}
	}
	return pix, w, h
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
