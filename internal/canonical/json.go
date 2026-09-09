package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxBytes bounds one canonical value before allocation-heavy parsing.
const MaxBytes = 1 << 20

// Bytes validates and serializes a JSON-compatible value. Integers must be safe
// across Go/Rust consumers; floats, duplicate keys and malformed Unicode fail.
func Bytes(value any) ([]byte, error) {
	if err := validText(reflect.ValueOf(value), 0); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return Normalize(raw)
}

func validText(v reflect.Value, depth int) error {
	if depth > 64 {
		return errors.New("value nesting exceeds 64")
	}
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		return errors.New("floating point values are outside canonical v1")
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return validText(v.Elem(), depth+1)
		}
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return errors.New("invalid UTF-8 string")
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := validText(v.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			if err := validText(it.Key(), depth+1); err != nil {
				return err
			}
			if err := validText(it.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				if err := validText(v.Field(i), depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Normalize rejects ambiguous JSON and returns its unique v1 byte representation.
// It never normalizes Unicode text or changes array order. Nesting is bounded.
func Normalize(raw []byte) ([]byte, error) {
	if len(raw) > MaxBytes || !utf8.Valid(raw) {
		return nil, errors.New("JSON size or UTF-8 invalid")
	}
	if err := stringsValid(raw); err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := read(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON data")
	}
	var out bytes.Buffer
	if err = write(&out, v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Decode rejects duplicate and unknown members before decoding into a typed
// destination. The caller remains responsible for semantic field validation.
func Decode(raw []byte, dst any) error {
	normal, err := Normalize(raw)
	if err != nil {
		return err
	}
	t := reflect.TypeOf(dst)
	if t == nil || t.Kind() != reflect.Pointer {
		return errors.New("decode destination must be a pointer")
	}
	if err := shape(normal, t.Elem()); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(normal))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

// encoding/json matches field names case-insensitively and tolerates missing
// members. Authority-bearing v1 records require exact names and explicit nulls.
func shape(raw []byte, t reflect.Type) error {
	if t == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(raw, []byte("null")) {
			return nil
		}
		return shape(raw, t.Elem())
	}
	if bytes.Equal(raw, []byte("null")) && t.Kind() != reflect.Interface {
		return errors.New("null for nonnullable field")
	}
	switch t.Kind() {
	case reflect.Struct:
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		if m == nil {
			return errors.New("null structure")
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			parts := strings.Split(tag, ",")
			name := parts[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			value, ok := m[name]
			if !ok {
				if strings.Contains(tag, ",omitempty") {
					continue
				}
				return fmt.Errorf("missing exact field %q", name)
			}
			if err := shape(value, f.Type); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			delete(m, name)
		}
		if len(m) != 0 {
			return errors.New("unknown exact field")
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var a []json.RawMessage
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		for _, v := range a {
			if err := shape(v, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// Hash binds canonical bytes to an ASCII domain. A digest is integrity evidence,
// not authentication or permission to execute an effect.
func Hash(domain string, value any) (string, error) {
	if domain == "" || strings.ContainsAny(domain, "\r\n") {
		return "", errors.New("invalid hash domain")
	}
	for _, c := range domain {
		if c < 33 || c > 126 {
			return "", errors.New("invalid hash domain")
		}
	}
	b, err := Bytes(value)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(domain + "\n"))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func read(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("JSON nesting exceeds 64")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, e
				}
				key, ok := k.(string)
				if !ok {
					return nil, errors.New("object key is not text")
				}
				for _, c := range key {
					if c > 127 {
						return nil, errors.New("non-ASCII object key")
					}
				}
				if _, ok = m[key]; ok {
					return nil, errors.New("duplicate object key")
				}
				v, e := read(d, depth+1)
				if e != nil {
					return nil, e
				}
				m[key] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, errors.New("unterminated object")
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, e := read(d, depth+1)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, errors.New("unterminated array")
			}
			return a, nil
		}
		return nil, errors.New("unexpected delimiter")
	case json.Number:
		n, e := strconv.ParseInt(string(x), 10, 64)
		if e != nil || n > 9007199254740991 || n < -9007199254740991 || string(x) == "-0" {
			return nil, errors.New("noncanonical integer domain")
		}
		return n, nil
	default:
		return t, nil
	}
}

func write(out *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			quote(out, k)
			out.WriteByte(':')
			if err := write(out, x[k]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := write(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case string:
		quote(out, x)
	case int64:
		out.WriteString(strconv.FormatInt(x, 10))
	case bool:
		out.WriteString(strconv.FormatBool(x))
	case nil:
		out.WriteString("null")
	default:
		return fmt.Errorf("unsupported JSON type %T", v)
	}
	return nil
}

func quote(out *bytes.Buffer, s string) {
	out.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(c)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if c < 32 {
				fmt.Fprintf(out, `\u%04x`, c)
			} else {
				out.WriteRune(c)
			}
		}
	}
	out.WriteByte('"')
}

// encoding/json replaces invalid UTF-16 escapes with U+FFFD. Reject those before
// decoding so distinct malformed inputs cannot acquire the same identity.
func stringsValid(raw []byte) error {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		i++
		for ; i < len(raw) && raw[i] != '"'; i++ {
			if raw[i] != '\\' {
				continue
			}
			i++
			if i >= len(raw) {
				return errors.New("truncated escape")
			}
			if raw[i] != 'u' {
				continue
			}
			if i+4 >= len(raw) {
				return errors.New("truncated Unicode escape")
			}
			u, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
			if err != nil {
				return err
			}
			i += 4
			if u >= 0xdc00 && u <= 0xdfff {
				return errors.New("unpaired low surrogate")
			}
			if u >= 0xd800 && u <= 0xdbff {
				if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return errors.New("unpaired high surrogate")
				}
				low, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
				if e != nil || low < 0xdc00 || low > 0xdfff {
					return errors.New("invalid surrogate pair")
				}
				i += 6
			}
		}
	}
	return nil
}
