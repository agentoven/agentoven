package documents

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"

	"github.com/ledongthuc/pdf"
)

// scanImage is the picture of one scanned page, as a model can be shown it.
type scanImage struct {
	Mime string
	Data []byte
}

// pdfScanImages finds the page pictures of a scanned PDF. A scanner or a "print to PDF" of a photo
// stores each page as one JPEG (a DCTDecode image), and a JPEG stream is a JPEG file, so it can be
// handed to a vision model as it is, with no rasteriser. Only that case is read: a page whose
// picture is stored any other way (JBIG2, CCITT fax, raw or Flate-compressed pixels, JPEG 2000)
// would need decoding and is reported, not guessed at (ADR-0039, "Not done").
//
// The library that reads the page structure does not expose a stream's raw bytes, so the JPEG
// streams are found by scanning the file for image objects and matched to pages by their width,
// height and length as the page's resources declare them. When that cannot match a page, the
// pictures are taken in file order if there are as many as pages.
func pdfScanImages(data []byte, pages []int, lim Limits) (found map[int]scanImage, problems map[int]string) {
	found, problems = map[int]scanImage{}, map[int]string{}
	raw := rawJPEGs(data, 4*lim.MaxPages+64)
	declared, total, err := declaredPageImages(data, pages)
	if err != nil {
		for _, p := range pages {
			problems[p] = "the page structure could not be read: " + err.Error()
		}
		return found, problems
	}
	used := map[int]bool{}
	for _, p := range pages {
		best, bestArea := -1, int64(-1)
		for _, d := range declared[p] {
			for i, r := range raw {
				if used[i] || r.w != d.w || r.h != d.h || (d.length > 0 && r.length > 0 && r.length != d.length) {
					continue
				}
				if area := d.w * d.h; area > bestArea {
					best, bestArea = i, area
				}
			}
		}
		if best < 0 && total == len(raw) && p <= len(raw) && !used[p-1] {
			best = p - 1 // as many pictures as pages: the n-th picture is the n-th page
		}
		if best < 0 {
			problems[p] = "its scan is not a JPEG picture this reader can extract (JBIG2, fax, raw or JPEG 2000 scans need a rasteriser)"
			continue
		}
		used[best] = true
		found[p] = scanImage{Mime: "image/jpeg", Data: raw[best].data}
	}
	return found, problems
}

type rawJPEG struct {
	w, h, length int64
	data         []byte
}

var (
	reDCT    = regexp.MustCompile(`/DCTDecode`)
	reImage  = regexp.MustCompile(`/Subtype\s*/Image`)
	reWidth  = regexp.MustCompile(`/Width\s+(\d+)`)
	reHeight = regexp.MustCompile(`/Height\s+(\d+)`)
	reLength = regexp.MustCompile(`/Length\s+(\d+)(\s+\d+\s+R)?`)
)

// rawJPEGs scans the file for image objects stored as DCTDecode and returns their streams, in file
// order, at most max of them.
func rawJPEGs(data []byte, max int) []rawJPEG {
	var out []rawJPEG
	for pos := 0; len(out) < max; {
		i := bytes.Index(data[pos:], []byte("stream"))
		if i < 0 {
			break
		}
		at := pos + i
		pos = at + len("stream")
		if at >= 3 && bytes.HasPrefix(data[at-3:], []byte("end")) {
			continue // the "endstream" keyword
		}
		start := pos
		switch {
		case bytes.HasPrefix(data[start:], []byte("\r\n")):
			start += 2
		case bytes.HasPrefix(data[start:], []byte("\n")), bytes.HasPrefix(data[start:], []byte("\r")):
			start++
		default:
			continue
		}
		winStart := max0(at - 2048)
		window := data[winStart:at]
		obj := bytes.LastIndex(window, []byte(" obj"))
		if obj < 0 {
			continue
		}
		dict := window[obj:]
		if !reDCT.Match(dict) || !reImage.Match(dict) {
			continue
		}
		w, h := intOf(reWidth, dict), intOf(reHeight, dict)
		length := int64(-1)
		if m := reLength.FindSubmatch(dict); m != nil && m[2] == nil {
			length, _ = strconv.ParseInt(string(m[1]), 10, 64)
		}
		end := -1
		if length > 0 && start+int(length) <= len(data) && bytes.Contains(data[start+int(length):min(len(data), start+int(length)+32)], []byte("endstream")) {
			end = start + int(length)
		} else if e := bytes.Index(data[start:], []byte("endstream")); e >= 0 {
			end = start + e
			for end > start && (data[end-1] == '\n' || data[end-1] == '\r') {
				end--
			}
		}
		if end < 0 || end-start < 4 || data[start] != 0xff || data[start+1] != 0xd8 {
			continue
		}
		out = append(out, rawJPEG{w: w, h: h, length: int64(end - start), data: data[start:end]})
		pos = end
	}
	return out
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func intOf(re *regexp.Regexp, b []byte) int64 {
	if m := re.FindSubmatch(b); m != nil {
		n, _ := strconv.ParseInt(string(m[1]), 10, 64)
		return n
	}
	return 0
}

type declaredImage struct{ w, h, length int64 }

// declaredPageImages lists, per page, the JPEG pictures the page's resources name.
func declaredPageImages(data []byte, pages []int) (out map[int][]declaredImage, total int, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, total, err = nil, 0, fmt.Errorf("%v", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, 0, err
	}
	total = r.NumPage()
	out = map[int][]declaredImage{}
	for _, p := range pages {
		if p < 1 || p > total {
			continue
		}
		xobjects := r.Page(p).Resources().Key("XObject")
		for _, name := range xobjects.Keys() {
			v := xobjects.Key(name)
			if v.Key("Subtype").Name() != "Image" || !hasFilter(v.Key("Filter"), "DCTDecode") {
				continue
			}
			out[p] = append(out[p], declaredImage{w: v.Key("Width").Int64(), h: v.Key("Height").Int64(), length: v.Key("Length").Int64()})
		}
	}
	return out, total, nil
}

func hasFilter(f pdf.Value, name string) bool {
	switch f.Kind() {
	case pdf.Name:
		return f.Name() == name
	case pdf.Array:
		for i := 0; i < f.Len(); i++ {
			if f.Index(i).Name() == name {
				return true
			}
		}
	}
	return false
}
