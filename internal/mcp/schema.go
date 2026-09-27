package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxSchemaDepth = 32
	maxSchemaNodes = 2000
	maxProblems    = 20
	maxSafeInteger = 1<<53 - 1
)

// Schema is the subset of JSON Schema 2020-12 that tool inputs and outputs
// use: one type per schema, object properties with required names, array
// items, enums, numeric bounds, string lengths and patterns. Objects never
// accept properties they do not list; they are always published with
// "additionalProperties": false.
type Schema struct {
	// Type is "object", "array", "string", "integer", "number", "boolean"
	// or "null".
	Type        string
	Title       string
	Description string

	Properties map[string]*Schema
	Required   []string

	Items    *Schema
	MinItems *int
	MaxItems *int

	// Enum lists the allowed values of a string, integer, number or boolean.
	Enum []any

	Minimum *float64
	Maximum *float64

	// MinLength and MaxLength count Unicode code points, not bytes.
	MinLength *int
	MaxLength *int
	// Pattern is an unanchored regular expression, as in JSON Schema. Use
	// syntax that Go's regexp package and ECMA-262 read the same way, and
	// anchor it with ^ and $ to constrain the whole value.
	Pattern string

	// Default documents the value a handler assumes when the property is
	// absent. The server does not fill it in.
	Default any

	re *regexp.Regexp
}

// MarshalJSON writes the schema as JSON Schema, with keys in a fixed order.
func (s Schema) MarshalJSON() ([]byte, error) {
	type wire struct {
		Type                 string             `json:"type"`
		Title                string             `json:"title,omitempty"`
		Description          string             `json:"description,omitempty"`
		Properties           map[string]*Schema `json:"properties,omitempty"`
		Required             []string           `json:"required,omitempty"`
		AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
		Items                *Schema            `json:"items,omitempty"`
		MinItems             *int               `json:"minItems,omitempty"`
		MaxItems             *int               `json:"maxItems,omitempty"`
		Enum                 []any              `json:"enum,omitempty"`
		Minimum              *float64           `json:"minimum,omitempty"`
		Maximum              *float64           `json:"maximum,omitempty"`
		MinLength            *int               `json:"minLength,omitempty"`
		MaxLength            *int               `json:"maxLength,omitempty"`
		Pattern              string             `json:"pattern,omitempty"`
		Default              any                `json:"default,omitempty"`
	}
	w := wire{
		Type: s.Type, Title: s.Title, Description: s.Description,
		Properties: s.Properties, Required: s.Required,
		Items: s.Items, MinItems: s.MinItems, MaxItems: s.MaxItems,
		Enum: s.Enum, Minimum: s.Minimum, Maximum: s.Maximum,
		MinLength: s.MinLength, MaxLength: s.MaxLength, Pattern: s.Pattern,
		Default: s.Default,
	}
	if s.Type == "object" {
		w.AdditionalProperties = new(false)
	}
	return json.Marshal(w)
}

func typePhrase(t string) (string, bool) {
	switch t {
	case "object":
		return "an object", true
	case "array":
		return "an array", true
	case "string":
		return "a string", true
	case "integer":
		return "an integer", true
	case "number":
		return "a number", true
	case "boolean":
		return "true or false", true
	case "null":
		return "null", true
	}
	return "", false
}

// prepare checks a schema written by a tool author and returns a deep copy
// with its patterns compiled and its enum and default values normalised.
func (s *Schema) prepare(path string, depth int, nodes *int) (*Schema, error) {
	if s == nil {
		return nil, fmt.Errorf("%s: the schema is missing", path)
	}
	*nodes++
	if depth > maxSchemaDepth {
		return nil, fmt.Errorf("%s: schemas may nest at most %d levels", path, maxSchemaDepth)
	}
	if *nodes > maxSchemaNodes {
		return nil, fmt.Errorf("%s: a schema may have at most %d nodes", path, maxSchemaNodes)
	}
	if _, ok := typePhrase(s.Type); !ok {
		return nil, fmt.Errorf("%s: unknown type %q", path, s.Type)
	}
	if s.Type != "object" && (s.Properties != nil || s.Required != nil) {
		return nil, fmt.Errorf("%s: properties and required apply only to objects", path)
	}
	if s.Type != "array" && (s.Items != nil || s.MinItems != nil || s.MaxItems != nil) {
		return nil, fmt.Errorf("%s: items, minItems and maxItems apply only to arrays", path)
	}
	if s.Type != "string" && (s.MinLength != nil || s.MaxLength != nil || s.Pattern != "") {
		return nil, fmt.Errorf("%s: minLength, maxLength and pattern apply only to strings", path)
	}
	if s.Type != "integer" && s.Type != "number" && (s.Minimum != nil || s.Maximum != nil) {
		return nil, fmt.Errorf("%s: minimum and maximum apply only to numbers", path)
	}
	if err := checkBounds(path, s); err != nil {
		return nil, err
	}

	c := *s
	c.Properties, c.Required, c.Items, c.Enum, c.Default, c.re = nil, nil, nil, nil, nil, nil
	c.MinItems, c.MaxItems = clonePtr(s.MinItems), clonePtr(s.MaxItems)
	c.MinLength, c.MaxLength = clonePtr(s.MinLength), clonePtr(s.MaxLength)
	c.Minimum, c.Maximum = clonePtr(s.Minimum), clonePtr(s.Maximum)
	if s.Type == "object" {
		c.Properties = make(map[string]*Schema, len(s.Properties))
		folded := map[string]string{}
		for _, name := range sortedKeys(s.Properties) {
			if name == "" {
				return nil, fmt.Errorf("%s: a property has an empty name", path)
			}
			if other, dup := folded[strings.ToLower(name)]; dup {
				return nil, fmt.Errorf("%s: properties %q and %q differ only in case", path, other, name)
			}
			folded[strings.ToLower(name)] = name
			child, err := s.Properties[name].prepare(join(path, name), depth+1, nodes)
			if err != nil {
				return nil, err
			}
			c.Properties[name] = child
		}
		for i, name := range s.Required {
			if _, ok := s.Properties[name]; !ok {
				return nil, fmt.Errorf("%s: required property %q is not defined", path, name)
			}
			if slices.Contains(s.Required[:i], name) {
				return nil, fmt.Errorf("%s: required property %q is listed twice", path, name)
			}
		}
		c.Required = slices.Clone(s.Required)
	}
	if s.Items != nil {
		items, err := s.Items.prepare(path+"[]", depth+1, nodes)
		if err != nil {
			return nil, err
		}
		c.Items = items
	}
	if s.Pattern != "" {
		re, err := regexp.Compile(s.Pattern)
		if err != nil {
			return nil, fmt.Errorf("%s: pattern does not compile: %v", path, err)
		}
		c.re = re
	}
	if s.Enum != nil {
		if len(s.Enum) == 0 {
			return nil, fmt.Errorf("%s: enum must list at least one value", path)
		}
		switch s.Type {
		case "string", "integer", "number", "boolean":
		default:
			return nil, fmt.Errorf("%s: enum applies only to strings, numbers and booleans", path)
		}
		c.Enum = make([]any, len(s.Enum))
		for i, e := range s.Enum {
			n, err := roundTrip(e)
			if err != nil {
				return nil, fmt.Errorf("%s: enum value %d: %v", path, i, err)
			}
			c.Enum[i] = n
		}
		for i, e := range c.Enum {
			if err := c.check(e); err != nil {
				return nil, fmt.Errorf("%s: enum value %d does not fit the schema: %v", path, i, err)
			}
		}
	}
	if s.Default != nil {
		d, err := roundTrip(s.Default)
		if err != nil {
			return nil, fmt.Errorf("%s: default: %v", path, err)
		}
		if err := c.check(d); err != nil {
			return nil, fmt.Errorf("%s: default does not fit the schema: %v", path, err)
		}
		c.Default = d
	}
	return &c, nil
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	return new(*p)
}

func checkBounds(path string, s *Schema) error {
	for _, p := range []struct {
		name string
		v    *int
	}{{"minItems", s.MinItems}, {"maxItems", s.MaxItems}, {"minLength", s.MinLength}, {"maxLength", s.MaxLength}} {
		if p.v != nil && *p.v < 0 {
			return fmt.Errorf("%s: %s must not be negative", path, p.name)
		}
	}
	if s.MinItems != nil && s.MaxItems != nil && *s.MinItems > *s.MaxItems {
		return fmt.Errorf("%s: minItems is greater than maxItems", path)
	}
	if s.MinLength != nil && s.MaxLength != nil && *s.MinLength > *s.MaxLength {
		return fmt.Errorf("%s: minLength is greater than maxLength", path)
	}
	for _, f := range []*float64{s.Minimum, s.Maximum} {
		if f != nil && (math.IsNaN(*f) || math.IsInf(*f, 0)) {
			return fmt.Errorf("%s: minimum and maximum must be finite", path)
		}
	}
	if s.Minimum != nil && s.Maximum != nil && *s.Minimum > *s.Maximum {
		return fmt.Errorf("%s: minimum is greater than maximum", path)
	}
	return nil
}

// check validates a normalised value against a prepared schema and returns
// the first problem as an error.
func (s *Schema) check(v any) error {
	p := problems{root: "The value"}
	s.validate(v, "", &p)
	if len(p.list) > 0 {
		return fmt.Errorf("%s", p.list[0])
	}
	return nil
}

// roundTrip converts a Go value into the form decodeJSON produces, so that
// enum and default values compare like decoded arguments.
func roundTrip(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return decodeJSON(b)
}

func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// problems collects validation failures as plain-English sentences.
type problems struct {
	root  string
	list  []string
	extra int
}

func (p *problems) add(format string, args ...any) {
	if len(p.list) < maxProblems {
		p.list = append(p.list, fmt.Sprintf(format, args...))
		return
	}
	p.extra++
}

func (p *problems) subject(path string) string {
	if path == "" {
		return p.root
	}
	return strconv.Quote(path)
}

// summary joins the problems into one message.
func (p *problems) summary() string {
	if len(p.list) == 1 {
		return p.list[0]
	}
	var b strings.Builder
	for i, s := range p.list {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		b.WriteString(s)
	}
	if p.extra > 0 {
		fmt.Fprintf(&b, "\n- …and %d more.", p.extra)
	}
	return b.String()
}

// validate checks v, a value produced by decodeJSON, against a prepared
// schema. It returns v with integers rewritten in canonical form (so that
// 2.0 binds to a Go int) and records every problem it finds in p.
func (s *Schema) validate(v any, path string, p *problems) any {
	switch s.Type {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			p.add("%s must be an object, not %s.", p.subject(path), describe(v))
			return v
		}
		for _, k := range sortedKeys(obj) {
			if _, known := s.Properties[k]; !known {
				p.add("%s", unknownProperty(path, k, s.Properties))
			}
		}
		for _, k := range sortedKeys(s.Properties) {
			child, present := obj[k]
			if !present {
				if slices.Contains(s.Required, k) {
					p.add("%s is required.", strconv.Quote(join(path, k)))
				}
				continue
			}
			obj[k] = s.Properties[k].validate(child, join(path, k), p)
		}
		return obj
	case "array":
		arr, ok := v.([]any)
		if !ok {
			p.add("%s must be an array, not %s.", p.subject(path), describe(v))
			return v
		}
		if s.MinItems != nil && len(arr) < *s.MinItems {
			p.add("%s must have at least %s.", p.subject(path), plural(*s.MinItems, "item"))
		}
		if s.MaxItems != nil && len(arr) > *s.MaxItems {
			p.add("%s must have at most %s.", p.subject(path), plural(*s.MaxItems, "item"))
		}
		if s.Items != nil {
			for i := range arr {
				arr[i] = s.Items.validate(arr[i], fmt.Sprintf("%s[%d]", path, i), p)
			}
		}
		return arr
	case "string":
		str, ok := v.(string)
		if !ok {
			p.add("%s must be a string, not %s.", p.subject(path), describe(v))
			return v
		}
		n := utf8.RuneCountInString(str)
		if s.MinLength != nil && n < *s.MinLength {
			p.add("%s must be at least %s long.", p.subject(path), plural(*s.MinLength, "character"))
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			p.add("%s must be at most %s long.", p.subject(path), plural(*s.MaxLength, "character"))
		}
		if s.re != nil && !s.re.MatchString(str) {
			p.add("%s does not match the pattern %s.", p.subject(path), s.Pattern)
		}
		s.checkEnum(str, path, p)
		return str
	case "integer", "number":
		num, ok := v.(json.Number)
		if !ok {
			phrase, _ := typePhrase(s.Type)
			p.add("%s must be %s, not %s.", p.subject(path), phrase, describe(v))
			return v
		}
		// num is valid JSON, so ParseFloat can only fail on range: overflow
		// gives ±Inf and underflow gives zero, which is the right value.
		f, _ := strconv.ParseFloat(string(num), 64)
		if math.IsInf(f, 0) {
			p.add("%s is too large.", p.subject(path))
			return v
		}
		if s.Type == "integer" {
			if f != math.Trunc(f) {
				p.add("%s must be an integer, not %s.", p.subject(path), num)
				return v
			}
			if math.Abs(f) > maxSafeInteger {
				p.add("%s must be between %d and %d.", p.subject(path), -maxSafeInteger, maxSafeInteger)
				return v
			}
			num = json.Number(strconv.FormatInt(int64(f), 10))
		}
		if s.Minimum != nil && f < *s.Minimum {
			p.add("%s must be at least %s.", p.subject(path), formatFloat(*s.Minimum))
		}
		if s.Maximum != nil && f > *s.Maximum {
			p.add("%s must be at most %s.", p.subject(path), formatFloat(*s.Maximum))
		}
		s.checkEnum(num, path, p)
		return num
	case "boolean":
		b, ok := v.(bool)
		if !ok {
			p.add("%s must be true or false, not %s.", p.subject(path), describe(v))
			return v
		}
		s.checkEnum(b, path, p)
		return b
	case "null":
		if v != nil {
			p.add("%s must be null, not %s.", p.subject(path), describe(v))
		}
		return v
	}
	return v
}

func (s *Schema) checkEnum(v any, path string, p *problems) {
	if s.Enum == nil {
		return
	}
	for _, e := range s.Enum {
		if sameValue(e, v) {
			return
		}
	}
	if len(s.Enum) == 1 {
		p.add("%s must be %s.", p.subject(path), listValues(s.Enum))
		return
	}
	p.add("%s must be one of %s.", p.subject(path), listValues(s.Enum))
}

func sameValue(a, b any) bool {
	an, aNum := a.(json.Number)
	bn, bNum := b.(json.Number)
	if aNum && bNum {
		af, _ := strconv.ParseFloat(string(an), 64)
		bf, _ := strconv.ParseFloat(string(bn), 64)
		return af == bf
	}
	if aNum || bNum {
		return false
	}
	return a == b
}

func unknownProperty(path, key string, props map[string]*Schema) string {
	noun := "parameter"
	if path != "" {
		noun = "field"
	}
	name := strconv.Quote(join(path, key))
	if len(props) == 0 {
		if path == "" {
			return name + " is not a known parameter; this tool takes no parameters."
		}
		return name + " is not a known field; this object takes no fields."
	}
	return fmt.Sprintf("%s is not a known %s. Known %ss: %s.", name, noun, noun, strings.Join(sortedKeys(props), ", "))
}

func describe(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case string:
		return "a string"
	case json.Number:
		if strings.ContainsAny(string(v), ".eE") {
			return "a number"
		}
		return "an integer"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

func listValues(vals []any) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		b, _ := json.Marshal(v)
		parts[i] = string(b)
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
