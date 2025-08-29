package note

import (
	"fmt"
	"sort"
)

// RLE variant probing to systematically infer correct RATTA_RLE behavior.

// rleMode enumerations
type accumMode int

const (
	accumShift  accumMode = iota // (len <<7) + next
	accumSum                     // sum of 7-bit chunks (+1 each)
	accumLEB128                  // little-endian base-128 style
)

type extMode int

const (
	extNone   extMode = iota
	extFF16LE         // 0xFF followed by 16-bit little-endian length (raw, no +1)
)

// rleSpec describes a decoding hypothesis.
type rleSpec struct {
	Name                string
	ColorFirst          bool // ordering: Color then Length (and continuation lengths may omit color)
	Accum               accumMode
	Ext                 extMode
	ContinuationHighBit bool // high bit in length indicates continuation
	LengthIncludesOne   bool // add +1 to each 7-bit chunk
}

var rleSpecs = []rleSpec{
	{"legacy_shift_color_pair", false, accumShift, extNone, true, true},
	{"shift_colorFirst", true, accumShift, extNone, true, true},
	{"sum_color_pair", false, accumSum, extNone, true, true},
	{"sum_colorFirst", true, accumSum, extNone, true, true},
	{"leb128_color_pair", false, accumLEB128, extNone, true, true},
	{"leb128_colorFirst", true, accumLEB128, extNone, true, true},
	{"shift_ext_ff16_color_pair", false, accumShift, extFF16LE, true, true},
	{"leb128_ext_ff16_colorFirst", true, accumLEB128, extFF16LE, true, true},
}

// RLEProbeResult holds metrics for a spec.
type RLEProbeResult struct {
	Spec            rleSpec
	Pixels          []byte
	Err             error
	DarkRatio       float64
	RowDarkVar      float64
	TransitionScore float64
	Score           float64
}

// decodeWithSpec attempts decode using given spec
func decodeWithSpec(data []byte, w, h int, horiz bool, spec rleSpec) ([]byte, error) {
	if horiz {
		w, h = h, w
	}
	expected := w * h
	out := make([]byte, 0, expected)
	i := 0
	for i < len(data) && len(out) < expected {
		var color byte
		// Fetch first unit depending on ordering
		if spec.ColorFirst {
			if i >= len(data) {
				break
			}
			color = data[i]
			i++
			if i >= len(data) {
				break
			}
			lb := data[i]
			i++
			ln, consumed, ok := assembleLength(data, i, lb, spec)
			i += consumed
			if !ok {
				break
			}
			// write run
			remain := expected - len(out)
			if ln > remain {
				ln = remain
			}
			writeRun(&out, expected, w, color, ln)
			continue
		} else { // length+color pair ordering
			if i+1 >= len(data) {
				break
			}
			color = data[i]
			lb := data[i+1]
			i += 2
			ln, consumed, ok := assembleLength(data, i, lb, spec)
			i += consumed
			if !ok {
				break
			}
			remain := expected - len(out)
			if ln > remain {
				ln = remain
			}
			writeRun(&out, expected, w, color, ln)
		}
	}
	if len(out) != expected {
		return nil, fmt.Errorf("spec %s produced %d of %d", spec.Name, len(out), expected)
	}
	return out, nil
}

// assembleLength builds run length based on spec and first length byte lb; returns length and bytes consumed (beyond lb already consumed)
func assembleLength(data []byte, idx int, lb byte, spec rleSpec) (int, int, bool) {
	// Extended length handling
	if spec.Ext == extFF16LE && lb == 0xFF {
		if idx+1 >= len(data) {
			return 0, 0, false
		}
		ln := int(data[idx]) | int(data[idx+1])<<8
		if ln <= 0 {
			ln = 1
		}
		return ln, 2, true
	}
	// Base length from first byte
	base := int(lb & 0x7F)
	if spec.LengthIncludesOne {
		base += 1
	}
	length := base
	consumed := 0
	// Continuations
	if spec.ContinuationHighBit {
		switch spec.Accum {
		case accumShift:
			for (lb & 0x80) != 0 {
				if idx+consumed+1 >= len(data) {
					break
				}
				nb := data[idx+consumed]
				consumed++
				// if color-first ordering, continuation may be length-only; handled externally (we pass only length bytes here)
				seg := int(nb & 0x7F)
				if spec.LengthIncludesOne {
					seg += 1
				}
				length = (length << 7) + seg
				lb = nb
			}
		case accumSum:
			for (lb & 0x80) != 0 {
				if idx+consumed >= len(data) {
					break
				}
				nb := data[idx+consumed]
				consumed++
				seg := int(nb & 0x7F)
				if spec.LengthIncludesOne {
					seg += 1
				}
				length += seg
				lb = nb
			}
		case accumLEB128:
			shift := 7
			for (lb & 0x80) != 0 {
				if idx+consumed >= len(data) {
					break
				}
				nb := data[idx+consumed]
				consumed++
				seg := int(nb & 0x7F)
				if spec.LengthIncludesOne {
					seg += 1
				}
				length += seg << shift
				shift += 7
				lb = nb
			}
		}
	}
	return length, consumed, true
}

// Metrics
func metricDarkRatio(pix []byte) float64 {
	if len(pix) == 0 {
		return 0
	}
	d := 0
	for _, p := range pix {
		if p < 0x30 {
			d++
		}
	}
	return float64(d) / float64(len(pix))
}
func metricRowDarkVariance(pix []byte, w, h int) float64 {
	if w == 0 || h == 0 {
		return 0
	}
	rows := h
	if rows > 400 {
		rows = 400
	}
	vals := make([]float64, rows)
	for r := 0; r < rows; r++ {
		row := pix[r*w : (r+1)*w]
		dark := 0
		for _, p := range row {
			if p < 0x30 {
				dark++
			}
		}
		vals[r] = float64(dark) / float64(w)
	}
	// variance
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	mean := sum / float64(len(vals))
	exp := 0.0
	for _, v := range vals {
		d := v - mean
		exp += d * d
	}
	return exp / float64(len(vals))
}

// transitionScore already defined in parse.go; reused here.

// ProbeRLE runs all specs and returns ordered results.
func ProbeRLE(data []byte, w, h int, horiz bool) []RLEProbeResult {
	results := make([]RLEProbeResult, 0, len(rleSpecs))
	for _, spec := range rleSpecs {
		pix, err := decodeWithSpec(data, w, h, horiz, spec)
		res := RLEProbeResult{Spec: spec, Err: err}
		if err == nil {
			res.Pixels = pix
			res.DarkRatio = metricDarkRatio(pix)
			res.RowDarkVar = metricRowDarkVariance(pix, w, h)
			res.TransitionScore = transitionScore(pix, w, h)
			// composite score: penalize dark ratio & variance; reward transitions
			res.Score = res.TransitionScore - (res.DarkRatio * 50) - (res.RowDarkVar * 200)
		}
		results = append(results, res)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	return results
}

// ChooseBestSpec selects top scoring valid spec.
func ChooseBestSpec(results []RLEProbeResult) (rleSpec, bool) {
	for _, r := range results {
		if r.Err == nil {
			return r.Spec, true
		}
	}
	return rleSpec{}, false
}
