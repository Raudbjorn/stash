package aitag

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// EncodePNG turns a raw RGB frame into a lossless PNG.
func EncodePNG(rgb []byte, width, height int) ([]byte, error) {
	if len(rgb) != width*height*3 {
		return nil, fmt.Errorf("frame is %d bytes, expected %d", len(rgb), width*height*3)
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			i := (y*width + x) * 3
			img.SetNRGBA(x, y, color.NRGBA{R: rgb[i], G: rgb[i+1], B: rgb[i+2], A: 255})
		}
	}

	var buf bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
