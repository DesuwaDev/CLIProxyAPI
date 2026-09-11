package wire

import (
	"golang.org/x/net/http2/hpack"
)

// h2HpackEncoder mirrors the hpack encoder of the Rust h2 crate (hyper):
//   - strings are always Huffman coded;
//   - name+value matches in the static or dynamic table are emitted as indexed;
//   - values of :path, age, content-length, etag, if-modified-since,
//     if-none-match, location and set-cookie are never inserted into the
//     dynamic table (literal without indexing, nghttp2 heuristic);
//   - entries larger than 3/4 of the table are not inserted;
//   - everything else is inserted with incremental indexing.
//
// The Go hpack.Encoder differs on the first and third points, which changes the
// header block bytes even when the header list is identical.
type h2HpackEncoder struct {
	maxSize uint32
	size    uint32
	// dynamic table, newest first (index 62 is dyn[0])
	dyn []hpackEntry
	// pendingSizeUpdate emits a dynamic table size update before the next block.
	pendingSizeUpdate bool
}

type hpackEntry struct{ name, value string }

func (e hpackEntry) size() uint32 { return uint32(len(e.name) + len(e.value) + 32) }

func newH2HpackEncoder() *h2HpackEncoder { return &h2HpackEncoder{maxSize: 4096} }

// setMaxSize applies the peer's SETTINGS_HEADER_TABLE_SIZE.
func (e *h2HpackEncoder) setMaxSize(v uint32) {
	if v == e.maxSize {
		return
	}
	e.maxSize = v
	e.pendingSizeUpdate = true
	e.evict()
}

var hpackSkipValueIndex = map[string]bool{
	":path":             true,
	"age":               true,
	"content-length":    true,
	"etag":              true,
	"if-modified-since": true,
	"if-none-match":     true,
	"location":          true,
	"set-cookie":        true,
}

// hpackStaticTable is RFC 7541 Appendix A (index 1..61).
var hpackStaticTable = []hpackEntry{
	{":authority", ""}, {":method", "GET"}, {":method", "POST"}, {":path", "/"}, {":path", "/index.html"},
	{":scheme", "http"}, {":scheme", "https"}, {":status", "200"}, {":status", "204"}, {":status", "206"},
	{":status", "304"}, {":status", "400"}, {":status", "404"}, {":status", "500"}, {"accept-charset", ""},
	{"accept-encoding", "gzip, deflate"}, {"accept-language", ""}, {"accept-ranges", ""}, {"accept", ""}, {"access-control-allow-origin", ""},
	{"age", ""}, {"allow", ""}, {"authorization", ""}, {"cache-control", ""}, {"content-disposition", ""},
	{"content-encoding", ""}, {"content-language", ""}, {"content-length", ""}, {"content-location", ""}, {"content-range", ""},
	{"content-type", ""}, {"cookie", ""}, {"date", ""}, {"etag", ""}, {"expect", ""},
	{"expires", ""}, {"from", ""}, {"host", ""}, {"if-match", ""}, {"if-modified-since", ""},
	{"if-none-match", ""}, {"if-range", ""}, {"if-unmodified-since", ""}, {"last-modified", ""}, {"link", ""},
	{"location", ""}, {"max-forwards", ""}, {"proxy-authenticate", ""}, {"proxy-authorization", ""}, {"range", ""},
	{"referer", ""}, {"refresh", ""}, {"retry-after", ""}, {"server", ""}, {"set-cookie", ""},
	{"strict-transport-security", ""}, {"transfer-encoding", ""}, {"user-agent", ""}, {"vary", ""}, {"via", ""},
	{"www-authenticate", ""},
}

// staticIndex returns (index, fullMatch). A name-only hit returns the first
// static entry with that name, as h2 does.
func staticIndex(name, value string) (int, bool) {
	nameIdx := 0
	for i, entry := range hpackStaticTable {
		if entry.name != name {
			continue
		}
		if entry.value == value {
			return i + 1, true
		}
		if nameIdx == 0 {
			nameIdx = i + 1
		}
	}
	return nameIdx, false
}

func (e *h2HpackEncoder) dynamicIndex(name, value string) (full int, nameOnly int) {
	for i, entry := range e.dyn {
		if entry.name != name {
			continue
		}
		if entry.value == value {
			return 62 + i, 0
		}
		if nameOnly == 0 {
			nameOnly = 62 + i
		}
	}
	return 0, nameOnly
}

func (e *h2HpackEncoder) insert(entry hpackEntry) {
	e.dyn = append([]hpackEntry{entry}, e.dyn...)
	e.size += entry.size()
	e.evict()
}

func (e *h2HpackEncoder) evict() {
	for e.size > e.maxSize && len(e.dyn) > 0 {
		last := e.dyn[len(e.dyn)-1]
		e.dyn = e.dyn[:len(e.dyn)-1]
		e.size -= last.size()
	}
}

// encode appends the header block for fields to dst.
func (e *h2HpackEncoder) encode(dst []byte, fields []hpack.HeaderField) []byte {
	if e.pendingSizeUpdate {
		dst = appendInt(dst, 5, 0x20, uint64(e.maxSize))
		e.pendingSizeUpdate = false
	}
	for _, f := range fields {
		entry := hpackEntry{f.Name, f.Value}
		sIdx, sFull := staticIndex(f.Name, f.Value)
		switch {
		case hpackSkipValueIndex[f.Name]:
			// literal without indexing; the name is always static here.
			dst = appendInt(dst, 4, 0x00, uint64(sIdx))
			dst = appendString(dst, f.Value)
		case sFull:
			dst = appendInt(dst, 7, 0x80, uint64(sIdx))
		default:
			dFull, dName := e.dynamicIndex(f.Name, f.Value)
			if dFull != 0 {
				dst = appendInt(dst, 7, 0x80, uint64(dFull))
				continue
			}
			if entry.size()*4 > e.maxSize*3 {
				// too large to index: literal without indexing
				if sIdx != 0 {
					dst = appendInt(dst, 4, 0x00, uint64(sIdx))
				} else if dName != 0 {
					dst = appendInt(dst, 4, 0x00, uint64(dName))
				} else {
					dst = append(dst, 0x00)
					dst = appendString(dst, f.Name)
				}
				dst = appendString(dst, f.Value)
				continue
			}
			nameIdx := sIdx
			if nameIdx == 0 {
				nameIdx = dName
			}
			if nameIdx != 0 {
				dst = appendInt(dst, 6, 0x40, uint64(nameIdx))
			} else {
				dst = append(dst, 0x40)
				dst = appendString(dst, f.Name)
			}
			dst = appendString(dst, f.Value)
			e.insert(entry)
		}
	}
	return dst
}

// appendInt encodes i with an n-bit prefix and the given first-byte mask.
func appendInt(dst []byte, n byte, mask byte, i uint64) []byte {
	k := uint64((1 << n) - 1)
	if i < k {
		return append(dst, mask|byte(i))
	}
	dst = append(dst, mask|byte(k))
	i -= k
	for i >= 128 {
		dst = append(dst, byte(0x80|(i&0x7f)))
		i >>= 7
	}
	return append(dst, byte(i))
}

// appendString always Huffman-codes s like the h2 crate.
func appendString(dst []byte, s string) []byte {
	if s == "" {
		return append(dst, 0x00)
	}
	huffLen := hpack.HuffmanEncodeLength(s)
	dst = appendInt(dst, 7, 0x80, huffLen)
	return hpack.AppendHuffmanString(dst, s)
}
