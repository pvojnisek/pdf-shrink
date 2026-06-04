package main

import (
	"fmt"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Stats summarizes what a shrink run did.
type Stats struct {
	InSize       int64
	OutSize      int64
	StrippedKeys int
	ImagesShrunk int
}

// stripAppleKeys removes Apple Markup / PencilKit editing data (/PPK and any
// /AAPL... key) from a dictionary in place. Returns how many keys it removed.
func stripAppleKeys(d types.Dict) int {
	n := 0
	for k := range d {
		if k == "PPK" || strings.HasPrefix(k, "AAPL") {
			delete(d, k)
			n++
		}
	}
	return n
}

// Shrink reads inFile, strips Apple annotation bloat, downsamples large images,
// optimizes, and writes outFile. The original file is never modified.
func Shrink(inFile, outFile string) (*Stats, error) {
	st := &Stats{}

	ctx, err := api.ReadContextFile(inFile)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	// Walk every object in the xref table and scrub Apple keys + shrink images.
	for _, e := range ctx.Table {
		if e == nil || e.Object == nil {
			continue
		}
		switch o := e.Object.(type) {
		case types.Dict:
			st.StrippedKeys += stripAppleKeys(o)
		case types.StreamDict:
			st.StrippedKeys += stripAppleKeys(o.Dict)
			if shrunk, changed := downsampleImage(&o); changed {
				st.ImagesShrunk += shrunk
				e.Object = o // write the modified struct back
			}
		}
	}

	if err := api.OptimizeContext(ctx); err != nil {
		return nil, fmt.Errorf("optimize: %w", err)
	}
	if err := api.WriteContextFile(ctx, outFile); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	return st, nil
}
