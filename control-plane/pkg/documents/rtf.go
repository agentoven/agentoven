package documents

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// rtfSkipped are the destinations that hold no document text: font and colour tables, styles,
// metadata, headers and footers, pictures, embedded objects and field instructions.
var rtfSkipped = map[string]bool{
	"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true, "header": true, "footer": true,
	"headerl": true, "headerr": true, "footerl": true, "footerr": true, "pict": true, "object": true,
	"fldinst": true, "listtable": true, "listoverridetable": true, "rsidtbl": true, "generator": true,
	"themedata": true, "datastore": true, "latentstyles": true, "colorschememapping": true, "xmlnstbl": true,
}

var rtfSymbols = map[string]string{
	"par": "\n", "line": "\n", "tab": "\t", "cell": " | ", "row": "\n", "sect": "\n",
	"emdash": "—", "endash": "–", "emspace": " ", "enspace": " ", "bullet": "•",
	"lquote": "‘", "rquote": "’", "ldblquote": "“", "rdblquote": "”",
}

// cp1252 holds the printable characters of the 0x80-0x9f range, which Latin-1 leaves as controls.
var cp1252 = map[byte]rune{
	0x80: '€', 0x82: '‚', 0x83: 'ƒ', 0x84: '„', 0x85: '…', 0x86: '†', 0x87: '‡',
	0x88: 'ˆ', 0x89: '‰', 0x8a: 'Š', 0x8b: '‹', 0x8c: 'Œ', 0x8e: 'Ž', 0x91: '‘',
	0x92: '’', 0x93: '“', 0x94: '”', 0x95: '•', 0x96: '–', 0x97: '—', 0x98: '˜',
	0x99: '™', 0x9a: 'š', 0x9b: '›', 0x9c: 'œ', 0x9e: 'ž', 0x9f: 'Ÿ',
}

// extractRTF reads the text of an RTF file: groups are tracked so that destinations without text
// are skipped, \uN and \'hh escapes are decoded, and \par and table cell/row marks become line
// breaks. Formatting, images and fields' instructions are dropped.
func extractRTF(ctx context.Context, data []byte, lim Limits) (*Result, error) {
	if !strings.HasPrefix(strings.TrimSpace(string(data[:min(len(data), 16)])), "{\\rtf") {
		return nil, fmt.Errorf("documents: not an RTF file")
	}
	var (
		out    strings.Builder
		skip   = 0 // depth at which a skipped group started; 0 when not skipping
		depth  = 0
		uc     = []int{1} // \ucN: how many fallback characters follow a \u escape, per group
		maxOut = lim.MaxTextBytes * 2
	)
	for i := 0; i < len(data) && out.Len() < maxOut; i++ {
		if i&0xffff == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		switch ch := data[i]; ch {
		case '{':
			depth++
			if depth > lim.MaxXMLDepth {
				return nil, fmt.Errorf("%w: RTF groups nested deeper than %d", ErrTooLarge, lim.MaxXMLDepth)
			}
			uc = append(uc, uc[len(uc)-1])
		case '}':
			if skip == depth {
				skip = 0
			}
			if depth > 0 {
				depth--
				uc = uc[:len(uc)-1]
			}
		case '\r', '\n':
		case '\\':
			if i+1 >= len(data) {
				break
			}
			i++
			c := data[i]
			switch {
			case c == '\\' || c == '{' || c == '}':
				if skip == 0 {
					out.WriteByte(c)
				}
			case c == '*':
				if skip == 0 {
					skip = depth
				}
			case c == '\'':
				if i+2 < len(data) {
					if n, err := strconv.ParseUint(string(data[i+1:i+3]), 16, 8); err == nil && skip == 0 {
						out.WriteRune(cp1252Rune(byte(n)))
					}
					i += 2
				}
			case c == '~':
				if skip == 0 {
					out.WriteByte(' ')
				}
			case c == '-' || c == '_':
				if skip == 0 {
					out.WriteByte('-')
				}
			case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
				j := i
				for j < len(data) && (data[j] >= 'a' && data[j] <= 'z' || data[j] >= 'A' && data[j] <= 'Z') {
					j++
				}
				word := string(data[i:j])
				k := j
				if k < len(data) && (data[k] == '-' || data[k] >= '0' && data[k] <= '9') {
					k++
					for k < len(data) && data[k] >= '0' && data[k] <= '9' {
						k++
					}
				}
				param := string(data[j:k])
				if k < len(data) && data[k] == ' ' {
					k++ // the space that ends a control word is not text
				}
				i = k - 1
				switch {
				case rtfSkipped[word]:
					if skip == 0 {
						skip = depth
					}
				case word == "uc":
					if n, err := strconv.Atoi(param); err == nil && n >= 0 && n < 8 {
						uc[len(uc)-1] = n
					}
				case word == "u":
					if n, err := strconv.Atoi(param); err == nil {
						if n < 0 {
							n += 65536
						}
						if skip == 0 {
							out.WriteRune(rune(n))
						}
						// The fallback characters that follow are for readers that cannot show the escape.
						for f := 0; f < uc[len(uc)-1] && i+1 < len(data); f++ {
							i++
							if data[i] == '\\' && i+3 < len(data) && data[i+1] == '\'' {
								i += 3
							}
						}
					}
				default:
					if s, ok := rtfSymbols[word]; ok && skip == 0 {
						out.WriteString(s)
					}
				}
			}
		default:
			if skip == 0 {
				out.WriteByte(ch)
			}
		}
	}
	res := &Result{}
	c := newCollector(res, lim)
	for _, line := range strings.Split(sanitize(out.String()), "\n") {
		c.text(Block{Kind: "paragraph", Text: line})
	}
	if len(res.Blocks) == 0 && !res.Truncated {
		res.warn("the RTF file has no text")
	}
	return res, nil
}

func cp1252Rune(b byte) rune {
	if r, ok := cp1252[b]; ok {
		return r
	}
	return rune(b)
}
