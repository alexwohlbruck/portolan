package sync

// The registry document, order-preserved. registry.Config parses
// portolan.json into maps for the engine; rewriting GROUPS back needs
// what maps throw away — key order — because groups.py --write emits
// `json.dumps(cfg, indent=2)` over a dict that keeps file order, and the
// whole point of a patch run's registry rewrite is a small diff. So this
// file is an ordered JSON representation plus a serializer that matches
// Python's json.dumps byte for byte: 2-space indent, ", "/": "
// separators, ensure_ascii escapes, floats via repr, no trailing
// newline.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Obj is a JSON object with remembered key order. Values are one of:
// *Obj, []any, string, json.Number, bool, nil.
type Obj struct {
	keys []string
	m    map[string]any
}

func NewObj() *Obj { return &Obj{m: map[string]any{}} }

func (o *Obj) Keys() []string { return append([]string(nil), o.keys...) }

func (o *Obj) Get(k string) (any, bool) { v, ok := o.m[k]; return v, ok }

// Set keeps an existing key's position and appends a new one — dict
// assignment semantics, which the rewrite relies on.
func (o *Obj) Set(k string, v any) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

func (o *Obj) Delete(k string) {
	if _, ok := o.m[k]; !ok {
		return
	}
	delete(o.m, k)
	for i, kk := range o.keys {
		if kk == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *Obj) Has(k string) bool { _, ok := o.m[k]; return ok }

// Str returns a string value, "" when absent or not a string.
func (o *Obj) Str(k string) string {
	s, _ := o.m[k].(string)
	return s
}

// Clone is a deep copy — the rewrite mutates entries and the planner
// wants to diff against the original.
func (o *Obj) Clone() *Obj {
	c := NewObj()
	for _, k := range o.keys {
		c.Set(k, cloneVal(o.m[k]))
	}
	return c
}

func cloneVal(v any) any {
	switch t := v.(type) {
	case *Obj:
		return t.Clone()
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneVal(e)
		}
		return out
	default:
		return v
	}
}

// ParseDoc decodes JSON preserving object key order. Numbers stay
// json.Number, so an untouched value re-emits with its source spelling.
func ParseDoc(raw []byte) (*Obj, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(*Obj)
	if !ok {
		return nil, fmt.Errorf("registry document is not a JSON object")
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after registry document")
	}
	return obj, nil
}

// LoadDoc reads and parses a registry file order-preserved.
func LoadDoc(path string) (*Obj, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := ParseDoc(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := NewObj()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	default:
		return tok, nil // string, json.Number, bool, nil
	}
}

// MarshalDoc serializes exactly as Python's json.dumps(v, indent=2)
// does — the format every groups.py --write has produced, so a Go
// rewrite diffs only where the data moved.
func MarshalDoc(v any) []byte {
	var b bytes.Buffer
	writeVal(&b, v, 0)
	return b.Bytes()
}

func writeVal(b *bytes.Buffer, v any, depth int) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writePyString(b, t)
	case json.Number:
		b.WriteString(string(t))
	case float64:
		b.WriteString(pyFloat(t))
	case int:
		b.WriteString(strconv.Itoa(t))
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range t {
			ind(b, depth+1)
			writeVal(b, e, depth+1)
			if i < len(t)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		ind(b, depth)
		b.WriteByte(']')
	case *Obj:
		if len(t.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range t.keys {
			ind(b, depth+1)
			writePyString(b, k)
			b.WriteString(": ")
			writeVal(b, t.m[k], depth+1)
			if i < len(t.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		ind(b, depth)
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("regdoc: unhandled type %T", v))
	}
}

func ind(b *bytes.Buffer, depth int) {
	for i := 0; i < depth; i++ {
		b.WriteString("  ")
	}
}

// writePyString escapes like json.dumps with ensure_ascii=True: the
// short escapes, \uXXXX for other control characters and everything
// past 0x7E, surrogate pairs for astral codepoints.
func writePyString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r >= 0x20 && r <= 0x7E:
				b.WriteByte(byte(r))
			case r > 0xFFFF: // surrogate pair
				r -= 0x10000
				fmt.Fprintf(b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
			default:
				fmt.Fprintf(b, `\u%04x`, r)
			}
		}
	}
	b.WriteByte('"')
}

// pyFloat formats a float the way Python's repr does for the magnitudes
// a bbox holds: shortest round-trip decimal, and a trailing ".0" on
// integral values ("40.0", where Go would say "40").
func pyFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// round4 mirrors Python's round(x, 4): the correctly-rounded 4-decimal
// value of the binary double.
func round4(x float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 4, 64), 64)
	return v
}
