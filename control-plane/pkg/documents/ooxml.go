package documents

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// ooxmlPackage is an Office Open XML file (docx, xlsx): a zip of XML parts. It is the one place that
// opens such a zip, so the limits that make that safe are in one place:
//
//   - the number of entries and duplicate names are checked before anything is read (a zip with two
//     entries of one name shows different readers different content);
//   - a part is only read through a reader that stops at the part limit and at MaxRatio times the
//     part's compressed size, whatever its header claims, so a zip bomb costs at most that much;
//   - parts are found by fixed name or through the package's relationships, never by a path taken
//     from the archive, so a name such as ../../x is never opened or written anywhere;
//   - an archive inside the archive (an embedded workbook, say) is never opened: nesting depth is
//     one by construction.
type ooxmlPackage struct {
	zr    *zip.Reader
	parts map[string]*zip.File
	lim   Limits
}

func openPackage(data []byte, lim Limits) (*ooxmlPackage, error) {
	if bytes.HasPrefix(data, []byte{0xd0, 0xcf, 0x11, 0xe0}) {
		return nil, fmt.Errorf("%w: this is a legacy binary Office file or an encrypted one; save it as .docx or .xlsx without a password", ErrUnsupported)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("documents: not a valid Office file (zip): %w", err)
	}
	if len(zr.File) > lim.MaxZipEntries {
		return nil, fmt.Errorf("%w: the archive has %d entries, the limit is %d", ErrTooLarge, len(zr.File), lim.MaxZipEntries)
	}
	p := &ooxmlPackage{zr: zr, parts: make(map[string]*zip.File, len(zr.File)), lim: lim}
	for _, f := range zr.File {
		name := partName(strings.ReplaceAll(f.Name, "\\", "/"))
		if _, dup := p.parts[name]; dup {
			return nil, fmt.Errorf("documents: not a valid Office file: the archive lists %q twice", f.Name)
		}
		p.parts[name] = f
	}
	if p.parts["[content_types].xml"] == nil {
		return nil, errors.New("documents: not an Office Open XML file: [Content_Types].xml is missing")
	}
	return p, nil
}

func partName(s string) string {
	return strings.ToLower(strings.TrimPrefix(path.Clean("/"+s), "/"))
}

// open returns a bounded reader for a part, or nil when the package has no such part.
func (p *ooxmlPackage) open(name string) (io.ReadCloser, error) {
	f := p.parts[partName(name)]
	if f == nil {
		return nil, nil
	}
	allowed := int64(p.lim.MaxRatio) * int64(f.CompressedSize64)
	if allowed < 64<<10 {
		allowed = 64 << 10 // a tiny part may legitimately expand more than the ratio says
	}
	if allowed > p.lim.MaxPartBytes {
		allowed = p.lim.MaxPartBytes
	}
	if f.UncompressedSize64 > uint64(allowed) {
		return nil, fmt.Errorf("%w: part %s expands to %d bytes from %d (limits: %d bytes, ratio %d)", ErrTooLarge, name, f.UncompressedSize64, f.CompressedSize64, p.lim.MaxPartBytes, p.lim.MaxRatio)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("documents: part %s is damaged: %w", name, err)
	}
	return &boundedReader{rc: rc, left: allowed, name: name}, nil
}

// boundedReader fails, rather than ends quietly, when a part expands past its allowance: a
// truncated read would be taken for a complete part.
type boundedReader struct {
	rc   io.ReadCloser
	left int64
	name string
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.left <= 0 {
		var one [1]byte
		if n, _ := b.rc.Read(one[:]); n > 0 {
			return 0, fmt.Errorf("%w: part %s expands past its limit", ErrTooLarge, b.name)
		}
		return 0, io.EOF
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *boundedReader) Close() error { return b.rc.Close() }

type relationship struct{ ID, Type, Target, Mode string }

func (p *ooxmlPackage) relationships(ctx context.Context, name string) ([]relationship, error) {
	rc, err := p.open(name)
	if err != nil || rc == nil {
		return nil, err
	}
	defer rc.Close()
	x := newXML(ctx, rc, p.lim.MaxXMLDepth)
	var rels []relationship
	for {
		tok, err := x.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return rels, nil
			}
			return nil, fmt.Errorf("documents: %s: %w", name, err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			rels = append(rels, relationship{ID: attr(se, "Id"), Type: attr(se, "Type"), Target: attr(se, "Target"), Mode: attr(se, "TargetMode")})
		}
	}
}

// mainPart is the part the package's root relationships name as the document (word/document.xml,
// xl/workbook.xml), found through them rather than assumed.
func (p *ooxmlPackage) mainPart(ctx context.Context, relSuffix string) (string, error) {
	rels, err := p.relationships(ctx, "_rels/.rels")
	if err != nil {
		return "", err
	}
	for _, r := range rels {
		if strings.HasSuffix(r.Type, relSuffix) && r.Mode != "External" {
			return resolveTarget("", r.Target), nil
		}
	}
	return "", fmt.Errorf("documents: not a valid Office file: no %s part is declared", relSuffix)
}

// resolveTarget makes a relationship target a package path: absolute targets are from the package
// root, relative ones from the directory of the part that holds the relationship.
func resolveTarget(baseDir, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(path.Clean(target), "/")
	}
	return path.Clean(path.Join(baseDir, target))
}

// xmlStream wraps the decoder with the checks every part gets: a document type declaration is
// refused (so no entity is ever defined, let alone one that names a file or a URL; encoding/xml
// resolves none, and refusing them makes that a tested property rather than a library detail),
// nesting is bounded, and a cancelled context stops the read.
type xmlStream struct {
	ctx   context.Context
	d     *xml.Decoder
	depth int
	max   int
	n     int
}

func newXML(ctx context.Context, r io.Reader, maxDepth int) *xmlStream {
	d := xml.NewDecoder(r)
	d.Strict = true
	return &xmlStream{ctx: ctx, d: d, max: maxDepth}
}

func (x *xmlStream) next() (xml.Token, error) {
	if x.n++; x.n&0x3ff == 0 {
		if err := x.ctx.Err(); err != nil {
			return nil, err
		}
	}
	tok, err := x.d.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case xml.Directive:
		if d := bytes.ToUpper(bytes.TrimSpace(t)); bytes.HasPrefix(d, []byte("DOCTYPE")) || bytes.HasPrefix(d, []byte("ENTITY")) {
			return nil, errors.New("a document type declaration is not allowed")
		}
	case xml.StartElement:
		if x.depth++; x.depth > x.max {
			return nil, fmt.Errorf("%w: XML nested deeper than %d", ErrTooLarge, x.max)
		}
	case xml.EndElement:
		x.depth--
	}
	return tok, nil
}

// skip consumes the rest of the element whose start was just read.
func (x *xmlStream) skip() error {
	for depth := 1; depth > 0; {
		tok, err := x.next()
		if err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// text reads the character data of the element whose start was just read, through its end.
func (x *xmlStream) text() (string, error) {
	var b strings.Builder
	for depth := 1; depth > 0; {
		tok, err := x.next()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			b.Write(t)
		}
	}
	return b.String(), nil
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
