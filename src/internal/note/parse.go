package note

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Clean minimal parser + RATTA_RLE decoder (prototype)

var sigPattern = regexp.MustCompile(`SN_FILE_VER_\d{8}`)

const (
	addressSize = 4
	pageWidth   = 1404
	pageHeight  = 1872
)

type Notebook struct {
	Signature string
	W         int
	H         int
	Footer    map[string]any
	Pages     []PageMeta
}

type PageMeta struct{ Params map[string]string }

var fileReader io.ReadSeeker

func Parse(r io.ReadSeeker) (*Notebook, error) {
	fileReader = r
	buf := make([]byte, 64)
	if _, err := r.Read(buf); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	sig := sigPattern.Find(buf)
	if sig == nil {
		return nil, fmt.Errorf("signature not found")
	}
	if _, err := r.Seek(-addressSize, io.SeekEnd); err != nil {
		return nil, err
	}
	var footerAddr uint32
	if err := binary.Read(r, binary.LittleEndian, &footerAddr); err != nil {
		return nil, err
	}
	footer, err := readMeta(r, int64(footerAddr))
	if err != nil {
		return nil, fmt.Errorf("footer: %w", err)
	}
	var pageAddrs []int64
	for k, v := range footer {
		if strings.HasPrefix(k, "PAGE") {
			pageAddrs = append(pageAddrs, toInt64(v))
		}
	}
	sort.Slice(pageAddrs, func(i, j int) bool { return pageAddrs[i] < pageAddrs[j] })
	pages := make([]PageMeta, 0, len(pageAddrs))
	for _, a := range pageAddrs {
		pm, e := readMeta(r, a)
		if e != nil {
			return nil, e
		}
		pages = append(pages, PageMeta{Params: pm})
	}
	fAny := map[string]any{}
	for k, v := range footer {
		fAny[k] = v
	}
	// Determine page dimensions: default standard; allow env override for experimental high-res.
	sigStr := string(sig)
	width := pageWidth
	height := pageHeight
	if os.Getenv("SUPERNOTE_FORCE_HIRES") == "1" {
		width, height = 1920, 2560
	}
	return &Notebook{Signature: sigStr, W: width, H: height, Footer: fAny, Pages: pages}, nil
}

func (nb *Notebook) DecodePage(idx int) (*GrayImage, error) {
	if idx < 0 || idx >= len(nb.Pages) {
		return nil, fmt.Errorf("page index out of range")
	}
	mainImg, bgImg, err := nb.DecodeLayers(idx)
	if err != nil {
		return nil, err
	}
	if bgImg != nil {
		Composite(mainImg, bgImg)
	}
	return mainImg, nil
}

// DecodeLayers returns the raw main and background layer images (background may be nil).
func (nb *Notebook) DecodeLayers(idx int) (*GrayImage, *GrayImage, error) {
	if idx < 0 || idx >= len(nb.Pages) {
		return nil, nil, fmt.Errorf("page index out of range")
	}
	pm := nb.Pages[idx]
	mainImg, err := nb.decodeLayerFromPage(pm, "MAINLAYER")
	if err != nil {
		return nil, nil, fmt.Errorf("main layer: %w", err)
	}
	var bgImg *GrayImage
	if _, ok := pm.Params["BGLAYER"]; ok {
		if b, err := nb.decodeLayerFromPage(pm, "BGLAYER"); err == nil {
			bgImg = b
		} else {
			log.Printf("background decode failed: %v", err)
		}
	}
	return mainImg, bgImg, nil
}

// Composite overlays main over bg using alpha mask (alpha==0 means take background).
func Composite(main, bg *GrayImage) {
	if main == nil || bg == nil || main.alpha == nil {
		return
	}
	m := main.pix
	a := main.alpha
	bp := bg.pix
	replaced := 0
	for i := 0; i < len(m) && i < len(bp) && i < len(a); i++ {
		if a[i] == 0 { // transparent pixel -> pull from background
			m[i] = bp[i]
			a[i] = 255
			replaced++
		}
	}
	if os.Getenv("TRACE_BG") == "1" {
		log.Printf("background composite: replaced=%d alphaMode", replaced)
	}
}

// FlattenBackground normalizes large-scale background banding while preserving darker template marks.
func FlattenBackground(bg *GrayImage) {
	if bg == nil || len(bg.pix) == 0 {
		return
	}
	hist := bg.Histogram()
	var bestVal byte
	bestCount := -1
	for g, c := range hist {
		if g >= 0xC0 {
			if c > bestCount {
				bestCount = c
				bestVal = g
			}
		}
	}
	if bestCount <= 0 {
		return
	}
	tone := bestVal
	// Determine threshold below which we preserve (template lines/dots)
	preserveCut := int(tone) - 24
	if preserveCut < 0 {
		preserveCut = 0
	}
	for i, p := range bg.pix {
		if int(p) >= preserveCut {
			bg.pix[i] = tone
		}
	}
}

// DecodeBackground returns only the decoded background layer (if present) using variant search.
func (nb *Notebook) DecodeBackground(idx int) (*GrayImage, error) {
	if idx < 0 || idx >= len(nb.Pages) {
		return nil, fmt.Errorf("page index out of range")
	}
	pm := nb.Pages[idx]
	if _, ok := pm.Params["BGLAYER"]; !ok {
		return nil, fmt.Errorf("no BGLAYER")
	}
	addrStr := pm.Params["BGLAYER"]
	la, _ := strconv.ParseInt(addrStr, 10, 64)
	meta, err := readMeta(fileReader, la)
	if err != nil {
		return nil, err
	}
	bmpStr := meta["LAYERBITMAP"]
	proto := meta["LAYERPROTOCOL"]
	if proto != "RATTA_RLE" {
		return nil, fmt.Errorf("bg protocol %s unsupported", proto)
	}
	bmpAddr, _ := strconv.ParseInt(bmpStr, 10, 64)
	if _, err := fileReader.Seek(bmpAddr, io.SeekStart); err != nil {
		return nil, err
	}
	var blockLen uint32
	if err := binary.Read(fileReader, binary.LittleEndian, &blockLen); err != nil {
		return nil, err
	}
	data := make([]byte, blockLen)
	if _, err := io.ReadFull(fileReader, data); err != nil {
		return nil, err
	}
	allBlank := false
	if style, ok := pm.Params["PAGESTYLE"]; ok && style == "style_white" && int(blockLen) == specialWhiteStyleBlockSize {
		allBlank = true
	}
	horiz := pm.Params["ORIENTATION"] == "1090"
	pix, w2, h2, err := decodeRattaRLERef(data, nb.W, nb.H, allBlank, horiz)
	if err != nil {
		return nil, err
	}
	if len(pix) != w2*h2 {
		if nb.W != pageWidth || nb.H != pageHeight { // fallback attempt
			if fpix, fw, fh, ferr := decodeRattaRLERef(data, pageWidth, pageHeight, allBlank, horiz); ferr == nil && len(fpix) == fw*fh {
				pix, w2, h2 = fpix, fw, fh
			} else {
				return nil, fmt.Errorf("ratta_ref decoded %d != %d (fallback err: %v)", len(pix), w2*h2, ferr)
			}
		} else {
			return nil, fmt.Errorf("ratta_ref decoded %d != %d", len(pix), w2*h2)
		}
	}
	// Build alpha where transparent sentinel 0xff => alpha 0 else 255
	alpha := make([]byte, len(pix))
	for i, v := range pix {
		if v == 0xff {
			alpha[i] = 0
		} else {
			alpha[i] = 255
		}
	}
	return &GrayImage{pix: pix, alpha: alpha, W: w2, H: h2}, nil
}

// decodeLayerFromPage looks up the layer meta via key (MAINLAYER/BGLAYER) then decodes bitmap by protocol.
func (nb *Notebook) decodeLayerFromPage(pm PageMeta, key string) (*GrayImage, error) {
	addrStr, ok := pm.Params[key]
	if !ok {
		return nil, fmt.Errorf("layer key %s missing", key)
	}
	la, _ := strconv.ParseInt(addrStr, 10, 64)
	meta, err := readMeta(fileReader, la)
	if err != nil {
		return nil, err
	}
	bmpStr, okB := meta["LAYERBITMAP"]
	proto := meta["LAYERPROTOCOL"]
	// Decode layer bitmap by protocol
	if !okB {
		return nil, fmt.Errorf("layer bitmap missing in %s meta", key)
	}
	bmpAddr, _ := strconv.ParseInt(bmpStr, 10, 64)
	if _, err := fileReader.Seek(bmpAddr, io.SeekStart); err != nil {
		return nil, err
	}
	var blockLen uint32
	if err := binary.Read(fileReader, binary.LittleEndian, &blockLen); err != nil {
		return nil, err
	}
	// Load bitmap data block
	if blockLen < 16 { // heuristic: not a real bitmap, maybe style ref => synthesize neutral background
		if key == "BGLAYER" {
			pix := make([]byte, pageWidth*pageHeight)
			alp := make([]byte, pageWidth*pageHeight)
			for i := range pix {
				pix[i] = 0xfe
				alp[i] = 255
			}
			return &GrayImage{pix: pix, alpha: alp, W: pageWidth, H: pageHeight}, nil
		}
	}
	data := make([]byte, blockLen)
	if _, err := io.ReadFull(fileReader, data); err != nil {
		return nil, err
	}
	horiz := pm.Params["ORIENTATION"] == "1090"
	// Detect embedded PNG (signature 89 50 4E 47 0D 0A 1A 0A) even if protocol claims RATTA_RLE.
	pngSig := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	if len(data) >= 8 && bytes.Equal(data[:8], pngSig) {
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("png decode: %w", err)
		}
		r := img.Bounds()
		w2, h2 := r.Dx(), r.Dy()
		pix := make([]byte, w2*h2)
		alp := make([]byte, w2*h2)
		for y := 0; y < h2; y++ {
			for x := 0; x < w2; x++ {
				rc, gc, bc, ac := img.At(x, y).RGBA()
				// Convert 16-bit RGBA components (0-65535) to 8-bit then luminance
				R := int(rc >> 8)
				G := int(gc >> 8)
				B := int(bc >> 8)
				A := int(ac >> 8)
				Y := (299*R + 587*G + 114*B) / 1000
				if Y > 255 {
					Y = 255
				}
				i := y*w2 + x
				pix[i] = byte(Y)
				alp[i] = byte(A)
			}
		}
		// Optional rotation if orientation flag set and env opts request
		rot := os.Getenv("SUPERNOTE_PNG_ROTATE") // values: auto, cw, ccw, none
		if rot == "auto" && horiz {
			rot = "cw"
		} // auto uses orientation flag
		if rot == "cw" || rot == "ccw" { // perform 90-degree rotation
			outW, outH := h2, w2
			rpix := make([]byte, outW*outH)
			ralp := make([]byte, outW*outH)
			for y := 0; y < h2; y++ {
				for x := 0; x < w2; x++ {
					iOld := y*w2 + x
					var nx, ny int
					if rot == "cw" { // (x,y) -> (h2-1-y, x)
						nx = h2 - 1 - y
						ny = x
					} else { // ccw: (x,y)->(y, w2-1-x)
						nx = y
						ny = w2 - 1 - x
					}
					iNew := ny*outW + nx
					rpix[iNew] = pix[iOld]
					ralp[iNew] = alp[iOld]
				}
			}
			pix, alp = rpix, ralp
			w2, h2 = outW, outH
		}
		// PNG layer decoded successfully
		return &GrayImage{pix: pix, alpha: alp, W: w2, H: h2}, nil
	}
	// Skip allBlank logic for adaptive decoder
	switch proto {
	case "RATTA_RLE":
		// Use the reference decoder with improved error handling
		var pix []byte
		var w2, h2 int
		var err error
		pix, w2, h2, err = decodeRattaRLERef(data, nb.W, nb.H, false, horiz)
		if err != nil {
			// If using non-standard dims, retry with standard dims on mismatch error
			if (nb.W != pageWidth || nb.H != pageHeight) && strings.Contains(err.Error(), "ratta_ref decoded") {
				if fpix, fw, fh, ferr := decodeRattaRLERef(data, pageWidth, pageHeight, false, horiz); ferr == nil {
					pix, w2, h2 = fpix, fw, fh
					log.Printf("fallback to standard dimensions after mismatch: %v", err)
					goto sizecheck
				}
			}
			return nil, err
		}
	sizecheck:
		expected := w2 * h2
		if len(pix) != expected {
			if nb.W != pageWidth || nb.H != pageHeight { // fallback attempt
				if fpix, fw, fh, ferr := decodeRattaRLERef(data, pageWidth, pageHeight, false, horiz); ferr == nil && len(fpix) == fw*fh {
					pix, w2, h2 = fpix, fw, fh
				} else {
					return nil, fmt.Errorf("ratta_ref decoded %d != %d (fallback err: %v)", len(pix), expected, ferr)
				}
			} else {
				return nil, fmt.Errorf("ratta_ref decoded %d != %d (exact size required to prevent jaggedness)", len(pix), expected)
			}
		}

		// Validate row alignment to detect potential jaggedness causes
		if os.Getenv("VALIDATE_ROWS") == "1" {
			validateImageIntegrity(pix, w2, h2)
		}
		// Generate alpha from transparent sentinel 0xff
		alpha := make([]byte, len(pix))
		for i, v := range pix {
			if v == 0xff {
				alpha[i] = 0
			} else {
				alpha[i] = 255
			}
		}
		return &GrayImage{pix: pix, alpha: alpha, W: w2, H: h2}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol %s", proto)
	}
}

// decodeBackgroundVariants brute-forces alternative RATTA_RLE interpretations for BG layer.
func (nb *Notebook) decodeBackgroundVariants(pm PageMeta) (*GrayImage, error) {
	addrStr := pm.Params["BGLAYER"]
	la, _ := strconv.ParseInt(addrStr, 10, 64)
	meta, err := readMeta(fileReader, la)
	if err != nil {
		return nil, err
	}
	bmpStr := meta["LAYERBITMAP"]
	proto := meta["LAYERPROTOCOL"]
	if proto != "RATTA_RLE" {
		return nil, fmt.Errorf("bg protocol %s unsupported", proto)
	}
	bmpAddr, _ := strconv.ParseInt(bmpStr, 10, 64)
	if _, err := fileReader.Seek(bmpAddr, io.SeekStart); err != nil {
		return nil, err
	}
	var blockLen uint32
	if err := binary.Read(fileReader, binary.LittleEndian, &blockLen); err != nil {
		return nil, err
	}
	data := make([]byte, blockLen)
	if _, err := io.ReadFull(fileReader, data); err != nil {
		return nil, err
	}
	horiz := pm.Params["ORIENTATION"] == "1090"
	expected := pageWidth * pageHeight
	type variant struct {
		name   string
		pixels []byte
		err    error
	}
	variants := []variant{}
	p1, _, _, e1 := decodeRLEAdaptive(data, pageWidth, pageHeight, horiz)
	variants = append(variants, variant{"adaptive_sum", p1, e1})
	p2, _, _, e2 := legacyRLE(data, pageWidth, pageHeight)
	variants = append(variants, variant{"legacy", p2, e2})
	p3, _, _, e3 := rleShiftContinuation(data, pageWidth, pageHeight, horiz)
	variants = append(variants, variant{"shift_chain", p3, e3})
	p4, _, _, e4 := rleLEB128(data, pageWidth, pageHeight, horiz)
	variants = append(variants, variant{"leb128", p4, e4})
	p5, _, _, e5 := rleColorSingleChain(data, pageWidth, pageHeight, horiz)
	variants = append(variants, variant{"color_single", p5, e5})
	p6, _, _, e6 := rleColorSingleChainExt(data, pageWidth, pageHeight, horiz)
	variants = append(variants, variant{"color_single_ext", p6, e6})
	// Run systematic probe specs too
	probe := ProbeRLE(data, pageWidth, pageHeight, horiz)
	// Allow manual override via env RLE_SPEC
	if name := os.Getenv("RLE_SPEC"); name != "" {
		for _, res := range probe {
			if res.Spec.Name == name && res.Err == nil {
				// BG using manual spec
				return &GrayImage{pix: res.Pixels, W: pageWidth, H: pageHeight}, nil
			}
		}
		// RLE_SPEC override not found, falling back to auto
	}
	// Choose best scoring successful probe result
	if spec, ok := ChooseBestSpec(probe); ok {
		// Re-decode with chosen spec to ensure orientation same (probe already produced pixels)
		for _, r := range probe {
			if r.Spec == spec {
				// BG auto spec selected
				return &GrayImage{pix: r.Pixels, W: pageWidth, H: pageHeight}, nil
			}
		}
	}
	return nil, fmt.Errorf("probe failed all specs (expected %d)", expected)
}

// rleShiftContinuation replicates earlier primary algorithm (with shifting) for variant testing.
func rleShiftContinuation(data []byte, w, h int, horiz bool) ([]byte, int, int, error) {
	if horiz {
		w, h = h, w
	}
	expected := w * h
	out := make([]byte, 0, expected)
	i := 0
	for i < len(data) && len(out) < expected {
		if i+1 >= len(data) {
			break
		}
		c := data[i]
		l := data[i+1]
		i += 2
		acc := int(l&0x7F) + 1
		for (l & 0x80) != 0 {
			if i+1 >= len(data) {
				break
			}
			nc := data[i]
			nl := data[i+1]
			i += 2
			if nc != c { // flush, restart with new
				writeRun(&out, expected, w, c, acc)
				c = nc
				l = nl
				acc = int(l&0x7F) + 1
				continue
			}
			acc = (acc << 7) + (int(nl&0x7F) + 1)
			l = nl
		}
		remain := expected - len(out)
		if acc > remain {
			acc = remain
		}
		writeRun(&out, expected, w, c, acc)
	}
	if len(out) != expected {
		return out, w, h, fmt.Errorf("shift decoded %d != %d", len(out), expected)
	}
	return out, w, h, nil
}

// rleLEB128 interprets continuation bytes as little-endian base-128 digits (low-order first accumulation).
func rleLEB128(data []byte, w, h int, horiz bool) ([]byte, int, int, error) {
	if horiz {
		w, h = h, w
	}
	expected := w * h
	out := make([]byte, 0, expected)
	i := 0
	for i < len(data) && len(out) < expected {
		if i+1 >= len(data) {
			break
		}
		c := data[i]
		l := data[i+1]
		i += 2
		// first part
		acc := int(l&0x7F) + 1
		shift := 7
		for (l & 0x80) != 0 {
			if i+1 >= len(data) {
				break
			}
			nc := data[i]
			nl := data[i+1]
			i += 2
			if nc != c { // color changed terminate this run early
				i -= 2
				break
			}
			acc += (int(nl&0x7F) + 1) << shift
			shift += 7
			l = nl
		}
		remain := expected - len(out)
		if acc > remain {
			acc = remain
		}
		writeRun(&out, expected, w, c, acc)
	}
	if len(out) != expected {
		return out, w, h, fmt.Errorf("leb128 decoded %d != %d", len(out), expected)
	}
	return out, w, h, nil
}

// rleColorSingleChain: interpret each run as (color)(len1)[len2...]; subsequent continuation bytes are length-only (no color byte).
func rleColorSingleChain(data []byte, w, h int, horiz bool) ([]byte, int, int, error) {
	if horiz {
		w, h = h, w
	}
	expected := w * h
	out := make([]byte, 0, expected)
	i := 0
	for i < len(data) && len(out) < expected {
		c := data[i]
		i++
		if i >= len(data) {
			break
		}
		lb := data[i]
		i++
		length := int(lb&0x7F) + 1
		shift := 7
		for (lb&0x80) != 0 && i < len(data) {
			lb = data[i]
			i++
			length += (int(lb&0x7F) + 1) << shift
			shift += 7
		}
		remain := expected - len(out)
		if length > remain {
			length = remain
		}
		writeRun(&out, expected, w, c, length)
	}
	if len(out) != expected {
		return out, w, h, fmt.Errorf("color_single decoded %d != %d", len(out), expected)
	}
	return out, w, h, nil
}

// rleColorSingleChainExt extends previous by handling 0xFF marker as 16-bit little-endian length following.
func rleColorSingleChainExt(data []byte, w, h int, horiz bool) ([]byte, int, int, error) {
	if horiz {
		w, h = h, w
	}
	expected := w * h
	out := make([]byte, 0, expected)
	i := 0
	for i < len(data) && len(out) < expected {
		c := data[i]
		i++
		if i >= len(data) {
			break
		}
		lb := data[i]
		i++
		var length int
		if lb == lenMark { // extended 16-bit length
			if i+1 >= len(data) {
				break
			}
			length = int(data[i]) | int(data[i+1])<<8
			i += 2
			if length <= 0 || length > 5_000_000 {
				length = longLen
			}
		} else {
			length = int(lb&0x7F) + 1
			shift := 7
			for (lb&0x80) != 0 && i < len(data) {
				lb = data[i]
				i++
				if lb == lenMark && i+1 < len(data) { // treat as extended tail
					length += int(data[i]) | int(data[i+1])<<8
					i += 2
					break
				}
				length += (int(lb&0x7F) + 1) << shift
				shift += 7
			}
		}
		remain := expected - len(out)
		if length > remain {
			length = remain
		}
		writeRun(&out, expected, w, c, length)
	}
	if len(out) != expected {
		return out, w, h, fmt.Errorf("color_single_ext decoded %d != %d", len(out), expected)
	}
	return out, w, h, nil
}

func blackRatio(pix []byte) float64 {
	if len(pix) == 0 {
		return 0
	}
	black := 0
	for _, p := range pix {
		if p < 0x20 {
			black++
		}
	}
	return float64(black) / float64(len(pix))
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// helpers
func toInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case uint32:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}
func readMeta(r io.ReadSeeker, addr int64) (map[string]string, error) {
	if addr == 0 {
		return map[string]string{}, nil
	}
	if _, e := r.Seek(addr, io.SeekStart); e != nil {
		return nil, e
	}
	var l uint32
	if e := binary.Read(r, binary.LittleEndian, &l); e != nil {
		return nil, e
	}
	b := make([]byte, l)
	if _, e := io.ReadFull(r, b); e != nil {
		return nil, e
	}
	return parseParams(string(b)), nil
}

var metaRe = regexp.MustCompile(`<([^:<>]+):([^:<>]*)>`)

func parseParams(s string) map[string]string {
	m := map[string]string{}
	for _, gr := range metaRe.FindAllStringSubmatch(s, -1) {
		if len(gr) == 3 {
			if _, ok := m[gr[1]]; !ok {
				m[gr[1]] = gr[2]
			}
		}
	}
	return m
}

// RATTA_RLE constants
const (
	colBlack                   = 0x61
	colBG                      = 0x62
	colDark                    = 0x63
	colGray                    = 0x64
	colWhite                   = 0x65
	colMBlack                  = 0x66
	colMDark                   = 0x67
	colMGray                   = 0x68
	lenMark                    = 0xFF
	longLen                    = 0x4000
	specialWhiteStyleBlockSize = 0x140e
)

// decodeRattaRLE implements the authoritative RATTA_RLE decoding based on the Python reference.
// Semantics:
//   - Stream of (color,lengthByte) pairs.
//   - If lengthByte == 0xFF -> special long run: 0x4000 (or 0x400 if allBlank hint) bytes.
//   - If lengthByte high bit (0x80) set: this pair is a holder; we combine with the next pair if it has the SAME color.
//     Combined length formula: 1 + next.length + (((holder.length & 0x7f)+1) << 7)
//   - Otherwise run length = (lengthByte + 1).
//   - Length accumulation does not chain beyond two pairs (mirrors reference behavior using a single holder tuple).
//   - Orientation: if horizontal flag set swap width/height before writing final image.
func decodeRattaRLE(data []byte, w, h int, allBlank bool, horiz bool) ([]byte, int, int, error) {
	if horiz {
		w, h = h, w
	}
	expected := w * h
	out := make([]byte, 0, expected)
	// queue semantics from python: we emit immediately rather than queue but preserve ordering
	i := 0
	haveHolder := false
	var holdColor, holdLen byte
	for i < len(data) && len(out) < expected {
		if i+1 >= len(data) {
			break
		}
		color := data[i]
		lb := data[i+1]
		i += 2
		used := false
		if haveHolder {
			pc, pl := holdColor, holdLen
			haveHolder = false
			if color == pc && (lb&0x80) == 0 { // merge only if next length not a holder itself
				length := 1 + int(lb&0x7F) + (((int(pl) & 0x7f) + 1) << 7)
				// cap to expected
				remain := expected - len(out)
				if length > remain {
					length = remain
				}
				writeRun(&out, expected, w, pc, length)
				used = true
			} else { // flush holder alone
				length := (((int(pl) & 0x7f) + 1) << 7)
				remain := expected - len(out)
				if length > remain {
					length = remain
				}
				writeRun(&out, expected, w, pc, length)
				// if current pair was also a holder keep it
				if (lb & 0x80) != 0 {
					holdColor = color
					holdLen = lb
					haveHolder = true
					continue
				}
			}
		}
		if used {
			continue
		}
		if lb == lenMark { // special long length
			length := longLen
			if os.Getenv("RLE_FIX_BG") == "1" && color == colBG { // experimental: interpret as single-row run to avoid partial-row slicing
				length = w
			}
			if allBlank {
				length = 0x400
			}
			remain := expected - len(out)
			if length > remain {
				length = remain
			}
			writeRun(&out, expected, w, color, length)
			continue
		}
		if (lb & 0x80) != 0 { // holder
			holdColor = color
			holdLen = lb
			haveHolder = true
			continue
		}
		length := int(lb) + 1
		remain := expected - len(out)
		if length > remain {
			length = remain
		}
		writeRun(&out, expected, w, color, length)
	}
	// tail adjustment if holder left over (mirror python _adjust_tail_length)
	if haveHolder && len(out) < expected {
		gap := expected - len(out)
		pl := holdLen
		adjusted := 0
		for i := 7; i >= 0; i-- { // reversed range(8)
			l := ((int(pl) & 0x7f) + 1) << i
			if l <= gap {
				adjusted = l
				break
			}
		}
		if adjusted > 0 {
			writeRun(&out, expected, w, holdColor, adjusted)
		}
	}
	if len(out) != expected {
		return nil, 0, 0, fmt.Errorf("ratta_rle decoded %d != %d", len(out), expected)
	}
	if os.Getenv("RLE_DEBUG") == "1" {
		validateRowAlignment(out, w, h)
		logColorStats(data)
	}
	return out, w, h, nil
}

// decodeRattaRLERef is a closer line-by-line port of the Python reference logic for comparison.
func decodeRattaRLERef(data []byte, w, h int, allBlank bool, horiz bool) ([]byte, int, int, error) {
	if horiz {
		w, h = h, w
	}
	expected := w * h
	out := make([]byte, 0, expected)
	i := 0
	haveHolder := false
	var holdColor, holdLen byte
	analyzeRLEPositions(data)
	if os.Getenv("RLE_HEX") == "1" {
		limit := 256
		if len(data) < limit {
			limit = len(data)
		}
		hex := make([]byte, 0, limit*3)
		for i := 0; i < limit; i++ {
			hex = append(hex, fmt.Sprintf("%02X ", data[i])...)
		}
		// RLE hex debugging disabled
	}
	// optional dump of first pairs
	dumpPairs := os.Getenv("RLE_DUMP_PAIRS")
	dumped := 0
	for i < len(data) && len(out) < expected {
		if i+1 >= len(data) {
			break
		}
		color := data[i]
		length := data[i+1]
		i += 2
		dataPushed := false
		if haveHolder {
			pc, pl := holdColor, holdLen
			haveHolder = false
			if color == pc { // merge
				mergedLen := 1 + int(length) + (((int(pl) & 0x7f) + 1) << 7)
				remain := expected - len(out)
				if mergedLen > remain {
					mergedLen = remain
				}
				writeRun(&out, expected, w, pc, mergedLen)
				dataPushed = true
			} else { // flush holder alone
				flushLen := 1 + (((int(pl) & 0x7f) + 1) << 7)
				remain := expected - len(out)
				if flushLen > remain {
					flushLen = remain
				}
				writeRun(&out, expected, w, pc, flushLen)
				// current pair not yet processed further
			}
		}
		if !dataPushed {
			if length == lenMark { // special
				special := longLen
				if os.Getenv("RLE_FIX_BG") == "1" && color == colBG {
					special = w
				}
				if allBlank {
					special = 0x400
				}
				remain := expected - len(out)
				if special > remain {
					special = remain
				}
				writeRun(&out, expected, w, color, special)
				dataPushed = true
			} else if (length & 0x80) != 0 { // holder
				holdColor = color
				holdLen = length
				haveHolder = true
			} else { // normal
				runLen := int(length) + 1
				remain := expected - len(out)
				if runLen > remain {
					runLen = remain
				}
				writeRun(&out, expected, w, color, runLen)
				dataPushed = true
			}
		}
		if dumpPairs == "1" && dumped < 60 {
			// Detailed RLE pair debugging disabled
			dumped++
		}
	}
	if haveHolder && len(out) < expected { // tail adjust
		gap := expected - len(out)
		pl := holdLen
		adjusted := 0
		for bit := 7; bit >= 0; bit-- {
			l := ((int(pl) & 0x7f) + 1) << bit
			if l <= gap {
				adjusted = l
				break
			}
		}
		if adjusted > 0 {
			writeRun(&out, expected, w, holdColor, adjusted)
		}
	}
	if len(out) != expected {
		return nil, 0, 0, fmt.Errorf("ratta_ref decoded %d != %d", len(out), expected)
	}
	return out, w, h, nil
}

// decodeRattaRLERowFill interprets 0xFF length bytes as "fill remainder of current row" (instead of huge longLen) to combat horizontal band artifacts.
// It ignores holder semantics (treating every pair independently) and is used heuristically when the stream starts with repeated (BG,0xFF) pairs.
func decodeRattaRLERowFill(data []byte, w, h int, horiz bool) ([]byte, int, int, error) {
	if horiz {
		w, h = h, w
	}
	expected := w * h
	out := make([]byte, 0, expected)
	i := 0
	for i+1 < len(data) && len(out) < expected {
		color := data[i]
		lb := data[i+1]
		i += 2
		run := 0
		if lb == 0xFF { // fill rest of row
			remainRow := w - (len(out) % w)
			if remainRow <= 0 {
				remainRow = w
			}
			run = remainRow
		} else {
			run = int(lb) + 1
		}
		// clamp if near end
		remain := expected - len(out)
		if run > remain {
			run = remain
		}
		writeRun(&out, expected, w, color, run)
	}
	if len(out) != expected { // pad remaining with last or background
		padColor := byte(colBG)
		if len(out) > 0 {
			padColor = out[len(out)-1]
		}
		for len(out) < expected {
			writeRun(&out, expected, w, padColor, 1)
		}
	}
	return out, w, h, nil
}

// grayLUT maps protocol color codes to grayscale. We deliberately map BG a shade off pure white
// so that (optional) post-processing can preserve a “paper” look distinct from true white.
var grayLUT = map[byte]byte{ // maps protocol codes to standardized grayscale (python parity)
	colBlack: 0x00, colMBlack: 0x00,
	colDark: 0x9d, colMDark: 0x9d,
	colGray: 0xc9, colMGray: 0xc9,
	colWhite: 0xfe,
	colBG:    0xff, // transparent sentinel (alpha=0)
	// X2 variant codes if they appear directly
	0x9d: 0x9d, 0xc9: 0xc9, 0x9e: 0x9d, 0xca: 0xc9,
}

// analyzeRLEPositions gathers frequency of bytes at even/odd indices to infer which position holds color codes (expected limited palette 0x61-0x68).
func analyzeRLEPositions(data []byte) {
	if os.Getenv("RLE_ANALYZE") != "1" {
		return
	}
	even := map[byte]int{}
	odd := map[byte]int{}
	for i := 0; i+1 < len(data); i += 2 {
		even[data[i]]++
		odd[data[i+1]]++
	}
	// helper to summarize counts of expected palette
	palette := []byte{0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68}
	var evenPal, oddPal int
	for _, c := range palette {
		evenPal += even[c]
		oddPal += odd[c]
	}
	// RLE analysis debugging disabled
	// If palette counts skew heavily to odd positions, likely reversed order (length,color) rather than (color,length).
	if evenPal < oddPal/4 { // heuristic threshold
		// log.Printf("RLE_ANALYZE: palette strongly in odd byte -> suggest interpreting as (length,color) ordering")
	}
}

func decodeRLE(data []byte, w, h int, horiz bool) ([]byte, int, int, error) {
	if horiz {
		w, h = h, w
	}
	expected := w * h
	out := make([]byte, 0, expected)
	i := 0
	runs := 0
	for i < len(data) && len(out) < expected {
		if i+1 >= len(data) {
			break
		}
		c := data[i]
		l := data[i+1]
		i += 2
		// Build length using base-128 style digits but with high-order first: accLen starts with first 7 bits +1.
		accLen := int(l&0x7F) + 1
		for (l & 0x80) != 0 { // continuation flag
			if i+1 >= len(data) {
				break
			}
			nc := data[i]
			nl := data[i+1]
			i += 2
			if nc != c { // color changed unexpectedly mid-continuation: flush current run and treat new pair fresh
				writeRun(&out, expected, w, c, accLen)
				runs++
				c = nc
				l = nl
				accLen = int(l&0x7F) + 1
				continue
			}
			// Shift previous length 7 bits, then add next digit (+1) (mirrors earlier two-part heuristic generalised)
			accLen = (accLen << 7) + (int(nl&0x7F) + 1)
			l = nl
		}
		// Heuristic: if lone 0xFF produced enormous length beyond remainder, cap; else if exactly lenMark and no continuation consumed treat as longLen.
		remain := expected - len(out)
		if accLen > remain {
			if l == lenMark && accLen > longLen*2 { // suspicious overshoot from many continuations
				accLen = longLen
			} else if accLen > remain { // still oversize: clamp to remaining
				accLen = remain
			}
		}
		writeRun(&out, expected, w, c, accLen)
		runs++
	}
	if len(out) != expected {
		// RLE primary decode fallback attempted
		// Fallback to legacy heuristic decoder
		fb, _, _, err := legacyRLE(data, w, h)
		if err == nil && len(fb) == expected {
			return fb, w, h, nil
		}
		return nil, 0, 0, fmt.Errorf("decoded %d != %d", len(out), expected)
	}
	if os.Getenv("RLE_DEBUG") == "1" {
		dumpRowStats(out, w, 10)
	}
	return out, w, h, nil
}

// legacyRLE reproduces earlier holder-based heuristic that produced legible handwriting.
func legacyRLE(data []byte, w, h int) ([]byte, int, int, error) {
	expected := w * h
	out := make([]byte, 0, expected)
	i := 0
	var holdColor, holdLen byte
	haveHold := false
	for i < len(data) && len(out) < expected {
		if i+1 >= len(data) {
			break
		}
		c := data[i]
		l := data[i+1]
		i += 2
		used := false
		if haveHold {
			pc, pl := holdColor, holdLen
			haveHold = false
			if c == pc {
				length := 1 + int(l) + (((int(pl) & 0x7F) + 1) << 7)
				writeRun(&out, expected, w, pc, length)
				used = true
			} else {
				length := 1 + (((int(pl) & 0x7F) + 1) << 7)
				writeRun(&out, expected, w, pc, length)
			}
		}
		if !used {
			if l == lenMark {
				writeRun(&out, expected, w, c, longLen)
			} else if l&0x80 != 0 {
				holdColor = c
				holdLen = l
				haveHold = true
			} else {
				writeRun(&out, expected, w, c, int(l)+1)
			}
		}
	}
	if haveHold && len(out) < expected {
		remaining := expected - len(out)
		base := ((int(holdLen) & 0x7F) + 1) << 7
		if base > remaining {
			base = remaining
		}
		writeRun(&out, expected, w, holdColor, base)
	}
	if len(out) != expected {
		return out, w, h, fmt.Errorf("fallback decoded %d != %d", len(out), expected)
	}
	return out, w, h, nil
}

// decodeRLEAdaptiveWithDims runs adaptive decoder with explicit dimensions (no orientation swap logic)
func decodeRLEAdaptiveWithDims(data []byte, w, h int) ([]byte, int, int, error) {
	return decodeRLEAdaptive(data, w, h, false)
}

// decodeRLEAdaptive attempts alternative interpretation aiming to reduce horizontal artifacts on background layers.
// Strategy: treat 0xFF length marker followed by two bytes as extended little-endian length (common RLE pattern)
// and ignore continuation merging across differing colors (reset accumulator when color changes) rather than shifting.
func decodeRLEAdaptive(data []byte, w, h int, horiz bool) ([]byte, int, int, error) {
	if horiz {
		w, h = h, w
	}
	expected := w * h
	out := make([]byte, 0, expected)
	i := 0
	for i < len(data) && len(out) < expected {
		if i+1 >= len(data) {
			break
		}
		c := data[i]
		l := data[i+1]
		i += 2
		runLen := 0
		if l == lenMark { // extended length (next 2 bytes LE)
			if i+1 >= len(data) {
				break
			}
			runLen = int(data[i]) | int(data[i+1])<<8
			i += 2
			if runLen <= 0 || runLen > 1_000_000 {
				runLen = longLen
			}
		} else {
			// accumulate continuation chain but DO NOT shift; just sum segments
			runLen = int(l&0x7F) + 1
			for (l & 0x80) != 0 {
				if i+1 >= len(data) {
					break
				}
				nc := data[i]
				nl := data[i+1]
				i += 2
				if nc != c { // color changed: push back (rewind) and stop continuation
					i -= 2 // step back so outer loop reprocesses new pair
					break
				}
				runLen += int(nl&0x7F) + 1
				l = nl
			}
		}
		if runLen < 0 {
			runLen = 0
		}
		remain := expected - len(out)
		if runLen > remain {
			runLen = remain
		}
		writeRun(&out, expected, w, c, runLen)
	}
	if len(out) != expected {
		return nil, 0, 0, fmt.Errorf("adaptive decoded %d != %d", len(out), expected)
	}
	return out, w, h, nil
}

// transitionScore computes average transitions per row plus per column (normalized) to detect plausibility.
func transitionScore(pix []byte, w, h int) float64 {
	if w == 0 || h == 0 || len(pix) < w*h {
		return 0
	}
	sampleRows := h
	if sampleRows > 200 {
		sampleRows = 200
	}
	rowTrans := 0
	for r := 0; r < sampleRows; r++ {
		row := pix[r*w : (r+1)*w]
		t := 0
		for i := 1; i < w; i++ {
			if row[i] != row[i-1] {
				t++
			}
		}
		rowTrans += t
	}
	// sample some columns too
	colsSample := w
	if colsSample > 200 {
		colsSample = 200
	}
	colTrans := 0
	for c := 0; c < colsSample; c++ {
		prev := pix[c]
		t := 0
		for y := 1; y < h; y++ {
			v := pix[y*w+c]
			if v != prev {
				t++
				prev = v
			}
			if y >= 200 {
				break
			}
		}
		colTrans += t
	}
	return float64(rowTrans)/float64(sampleRows) + float64(colTrans)/float64(colsSample)
}

func writeRun(out *[]byte, expected, w int, code byte, length int) {
	if length <= 0 {
		return
	}
	g, known := grayLUT[code]
	if !known { // unknown -> treat as gray (not transparent) unless matches colBG
		if code == colBG {
			g = 0xff
		} else {
			g = 0xc9
		}
	}
	for length > 0 && len(*out) < expected {
		remain := expected - len(*out)
		if remain <= 0 {
			break
		}
		spaceInRow := w - (len(*out) % w)
		chunk := length
		if chunk > spaceInRow {
			chunk = spaceInRow
		}
		if chunk > remain {
			chunk = remain
		}
		start := len(*out)
		*out = append(*out, make([]byte, chunk)...)
		b := (*out)[start : start+chunk]
		for i := 0; i < chunk; i++ {
			b[i] = g
		}
		length -= chunk
	}
}

func dumpRowStats(pix []byte, w, rows int) {
	if rows*w > len(pix) {
		rows = len(pix) / w
	}
	for r := 0; r < rows; r++ {
		row := pix[r*w : (r+1)*w]
		// simple hash + left/right transitions count
		trans := 0
		for i := 1; i < w; i++ {
			if row[i] != row[i-1] {
				trans++
			}
		}
		// Row transition analysis disabled
	}
}

// validateRowAlignment logs if any run crosses more than one row boundary producing potential striping artifacts.
func validateRowAlignment(pix []byte, w, h int) {
	total := len(pix)
	if w == 0 || h == 0 || total != w*h {
		return
	}
	// Just compute simple checksum per row to spot repeating pattern lines.
	prev := -1
	repeat := 0
	for r := 0; r < h; r++ {
		row := pix[r*w : (r+1)*w]
		sum := 0
		for _, v := range row {
			sum += int(v)
		}
		if sum == prev {
			repeat++
		} else {
			if repeat > 10 {
				// Repeat rows detected
			}
			repeat = 0
		}
		prev = sum
		// detect if row uniform (potential blank stripe)
		uniform := true
		first := row[0]
		for i := 1; i < w; i++ {
			if row[i] != first {
				uniform = false
				break
			}
		}
		if uniform {
			// Uniform row detected
		}
	}
}

// logColorStats inspects raw RLE pairs for color code frequency (first byte of each pair) to diagnose unexpected palette usage producing stripes.
func logColorStats(data []byte) {
	counts := map[byte]int{}
	for i := 0; i+1 < len(data); i += 2 {
		counts[data[i]]++
	}
	if len(counts) == 0 {
		return
	}
	type kv struct {
		c byte
		n int
	}
	arr := make([]kv, 0, len(counts))
	for c, n := range counts {
		arr = append(arr, kv{c, n})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].n > arr[j].n })
	top := 0
	if len(arr) > 12 {
		top = 12
	} else {
		top = len(arr)
	}
	b := strings.Builder{}
	b.WriteString("RLE colors: ")
	for i := 0; i < top; i++ {
		b.WriteString(fmt.Sprintf("%02X:%d ", arr[i].c, arr[i].n))
	}
	log.Println(b.String())
}

// GrayImage implements image.Image
type GrayImage struct {
	pix   []uint8 // grayscale luminance 0=black 255=white
	alpha []uint8 // optional per-pixel alpha (0 transparent, 255 opaque); length == len(pix) when present
	W, H  int
}

func (g *GrayImage) ColorModel() color.Model { return color.GrayModel }
func (g *GrayImage) Bounds() image.Rectangle { return image.Rect(0, 0, g.W, g.H) }
func (g *GrayImage) At(x, y int) color.Color {
	if x < 0 || y < 0 || x >= g.W || y >= g.H {
		return color.Gray{}
	}
	idx := y*g.W + x
	if g.alpha != nil && idx < len(g.alpha) {
		// Return an RGBA with alpha preserved
		v := g.pix[idx]
		a := g.alpha[idx]
		return color.NRGBA{R: v, G: v, B: v, A: a}
	}
	return color.Gray{Y: g.pix[idx]}
}

// Pix returns a copy of the underlying pixel slice for read-only iteration.
func (g *GrayImage) Pix() []uint8 { return g.pix }

// Alpha returns the alpha slice (may be nil if not present)
func (g *GrayImage) Alpha() []uint8 { return g.alpha }

// SetPix sets a raw pixel (0-based index) if within bounds.
func (g *GrayImage) SetPix(i int, v uint8) {
	if i >= 0 && i < len(g.pix) {
		g.pix[i] = v
	}
}

// Histogram returns counts for each grayscale value present.
func (g *GrayImage) Histogram() map[byte]int {
	m := make(map[byte]int, 64)
	for _, p := range g.pix {
		m[p]++
	}
	return m
}

// UniformRowSample returns indices of up to n rows that are entirely one value (useful for stripe diagnosis).
func (g *GrayImage) UniformRowSample(n int) []int {
	if n <= 0 {
		return nil
	}
	res := make([]int, 0, n)
	for r := 0; r < g.H && len(res) < n; r++ {
		row := g.pix[r*g.W : (r+1)*g.W]
		first := row[0]
		uniform := true
		for i := 1; i < g.W; i++ {
			if row[i] != first {
				uniform = false
				break
			}
		}
		if uniform {
			res = append(res, r)
		}
	}
	return res
}

// validateImageIntegrity checks for patterns that might cause jaggedness
func validateImageIntegrity(pix []byte, w, h int) {
	if len(pix) != w*h {
		log.Printf("ERROR: pixel array size %d != expected %d", len(pix), w*h)
		return
	}

	// Check for various types of alignment issues
	discontinuities := 0
	suddenShifts := 0

	for y := 2; y < h-2; y++ {
		rowStart := y * w

		// Look for sudden jumps in pixel values that might indicate misalignment
		for x := 10; x < w-10; x++ {
			currIdx := rowStart + x
			curr := pix[currIdx]

			// Check surrounding pixels for anomalous patterns
			above := pix[(y-1)*w+x]
			below := pix[(y+1)*w+x]
			left := pix[currIdx-1]
			right := pix[currIdx+1]

			// Detect isolated dark pixels in light areas (potential misalignment artifacts)
			if curr < 100 && above > 200 && below > 200 && left > 200 && right > 200 {
				discontinuities++
			}

			// Detect horizontal line breaks that might indicate row shifts
			if y > 5 && y < h-5 {
				// Check if there's a horizontal line pattern that suddenly breaks
				rowAbove2 := pix[(y-2)*w+x]
				rowAbove1 := pix[(y-1)*w+x]
				rowBelow1 := pix[(y+1)*w+x]
				rowBelow2 := pix[(y+2)*w+x]

				// Pattern: dark line interrupted by light pixel
				if rowAbove2 < 150 && rowAbove1 < 150 && curr > 200 && rowBelow1 < 150 && rowBelow2 < 150 {
					suddenShifts++
				}
			}
		}
	}

	if discontinuities > w*h/10000 { // more than 0.01% of pixels
		log.Printf("WARNING: detected %d pixel discontinuities (potential misalignment artifacts)", discontinuities)
	}

	if suddenShifts > h/50 { // more than 2% of rows
		log.Printf("WARNING: detected %d horizontal line breaks (potential row shifts)", suddenShifts)
	}

	log.Printf("Image integrity detailed check: %d discontinuities, %d line breaks in %dx%d image", discontinuities, suddenShifts, w, h)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
