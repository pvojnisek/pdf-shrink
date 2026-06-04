package main

import (
	"bytes"
	"compress/zlib"
	"image"
	"image/jpeg"
	"io"
	"math"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	xdraw "golang.org/x/image/draw"
)

const (
	maxEdge = 2000 // downsample images whose longest edge exceeds this
	jpegQ   = 80
)

// nameOf returns a direct /Name value for key, or "" if missing/indirect/other.
func nameOf(d types.Dict, key string) string {
	o, ok := d.Find(key)
	if !ok {
		return ""
	}
	if n, ok := o.(types.Name); ok {
		return string(n)
	}
	return ""
}

// intOf returns an integer value for key, or 0 if missing/non-numeric.
func intOf(d types.Dict, key string) int {
	o, ok := d.Find(key)
	if !ok {
		return 0
	}
	switch v := o.(type) {
	case types.Integer:
		return int(v)
	case types.Float:
		return int(v)
	}
	return 0
}

// downsampleImage shrinks an oversized image XObject in place and re-encodes it
// as JPEG. It conservatively skips anything with masks, indirect colorspaces,
// predictors, or unusual bit depths so it never corrupts a document.
// Returns (1, true) if the image was modified.
func downsampleImage(sd *types.StreamDict) (int, bool) {
	d := sd.Dict
	if nameOf(d, "Subtype") != "Image" {
		return 0, false
	}
	// Skip anything that needs companion data we don't rewrite.
	for _, k := range []string{"SMask", "Mask", "ImageMask", "Decode"} {
		if _, ok := d.Find(k); ok {
			return 0, false
		}
	}
	w, h := intOf(d, "Width"), intOf(d, "Height")
	if w == 0 || h == 0 || (w <= maxEdge && h <= maxEdge) {
		return 0, false
	}
	cs := nameOf(d, "ColorSpace")
	if cs != "DeviceRGB" && cs != "DeviceGray" {
		return 0, false // indirect / indexed / CMYK / ICC → skip
	}

	var src image.Image
	switch nameOf(d, "Filter") {
	case "DCTDecode":
		m, err := jpeg.Decode(bytes.NewReader(sd.Raw))
		if err != nil {
			return 0, false
		}
		src = m
	case "FlateDecode":
		if _, ok := d.Find("DecodeParms"); ok {
			return 0, false // predictor present → skip
		}
		if intOf(d, "BitsPerComponent") != 8 {
			return 0, false
		}
		zr, err := zlib.NewReader(bytes.NewReader(sd.Raw))
		if err != nil {
			return 0, false
		}
		raw, err := io.ReadAll(zr)
		zr.Close()
		if err != nil {
			return 0, false
		}
		src = pixelsToImage(raw, w, h, cs)
		if src == nil {
			return 0, false
		}
	default:
		return 0, false
	}

	// Target dimensions preserving aspect ratio.
	scale := float64(maxEdge) / math.Max(float64(w), float64(h))
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 || nh < 1 {
		return 0, false
	}

	var buf bytes.Buffer
	if cs == "DeviceGray" {
		dst := image.NewGray(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
		if jpeg.Encode(&buf, dst, &jpeg.Options{Quality: jpegQ}) != nil {
			return 0, false
		}
	} else {
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
		if jpeg.Encode(&buf, dst, &jpeg.Options{Quality: jpegQ}) != nil {
			return 0, false
		}
	}

	// Only keep the result if it actually saved space.
	if buf.Len() >= len(sd.Raw) {
		return 0, false
	}

	sd.Raw = buf.Bytes()
	sd.Content = nil
	sd.FilterPipeline = []types.PDFFilter{{Name: "DCTDecode"}}
	d["Filter"] = types.Name("DCTDecode")
	delete(d, "DecodeParms")
	d["Width"] = types.Integer(nw)
	d["Height"] = types.Integer(nh)
	d["BitsPerComponent"] = types.Integer(8)
	l := int64(len(sd.Raw))
	sd.StreamLength = &l
	d["Length"] = types.Integer(l)
	return 1, true
}

// pixelsToImage builds an image from raw 8-bit pixel data (no predictor).
func pixelsToImage(raw []byte, w, h int, cs string) image.Image {
	switch cs {
	case "DeviceGray":
		if len(raw) < w*h {
			return nil
		}
		g := image.NewGray(image.Rect(0, 0, w, h))
		copy(g.Pix, raw[:w*h])
		return g
	case "DeviceRGB":
		if len(raw) < w*h*3 {
			return nil
		}
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < w*h; i++ {
			img.Pix[i*4] = raw[i*3]
			img.Pix[i*4+1] = raw[i*3+1]
			img.Pix[i*4+2] = raw[i*3+2]
			img.Pix[i*4+3] = 0xff
		}
		return img
	}
	return nil
}
