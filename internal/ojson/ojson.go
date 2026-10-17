// Package ojson reads and writes JSON while keeping object key order, so edits
// to files the user owns (like Claude's settings.json) produce minimal diffs.
package ojson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Member is one key/value pair of an Object.
type Member struct {
	Key   string
	Value any
}

// Object is an ordered JSON object. Values are *Object, []any, string,
// json.Number, bool or nil.
type Object struct{ Members []Member }

// Get returns the value for key.
func (o *Object) Get(key string) (any, bool) {
	for _, m := range o.Members {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// Set replaces the value for key, or appends it.
func (o *Object) Set(key string, v any) {
	for i, m := range o.Members {
		if m.Key == key {
			o.Members[i].Value = v
			return
		}
	}
	o.Members = append(o.Members, Member{key, v})
}

// Delete removes key.
func (o *Object) Delete(key string) {
	for i, m := range o.Members {
		if m.Key == key {
			o.Members = append(o.Members[:i], o.Members[i+1:]...)
			return
		}
	}
}

// Parse decodes a JSON document whose top level is an object.
func Parse(data []byte) (*Object, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return &Object{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(*Object)
	if !ok {
		return nil, fmt.Errorf("top-level JSON value is not an object")
	}
	return obj, nil
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
			obj := &Object{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				obj.Members = append(obj.Members, Member{key, val})
			}
			_, err := dec.Token()
			return obj, err
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			_, err := dec.Token()
			return arr, err
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	default:
		return t, nil
	}
}

// Marshal encodes with two-space indentation and a trailing newline.
func Marshal(o *Object) ([]byte, error) {
	var buf bytes.Buffer
	if err := write(&buf, o, ""); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func write(w io.Writer, v any, indent string) error {
	inner := indent + "  "
	switch t := v.(type) {
	case *Object:
		if len(t.Members) == 0 {
			_, err := io.WriteString(w, "{}")
			return err
		}
		io.WriteString(w, "{\n")
		for i, m := range t.Members {
			k, _ := json.Marshal(m.Key)
			fmt.Fprintf(w, "%s%s: ", inner, k)
			if err := write(w, m.Value, inner); err != nil {
				return err
			}
			if i < len(t.Members)-1 {
				io.WriteString(w, ",")
			}
			io.WriteString(w, "\n")
		}
		_, err := io.WriteString(w, indent+"}")
		return err
	case []any:
		if len(t) == 0 {
			_, err := io.WriteString(w, "[]")
			return err
		}
		io.WriteString(w, "[\n")
		for i, x := range t {
			io.WriteString(w, inner)
			if err := write(w, x, inner); err != nil {
				return err
			}
			if i < len(t)-1 {
				io.WriteString(w, ",")
			}
			io.WriteString(w, "\n")
		}
		_, err := io.WriteString(w, indent+"]")
		return err
	default:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(t); err != nil {
			return err
		}
		_, err := w.Write(bytes.TrimRight(buf.Bytes(), "\n"))
		return err
	}
}
