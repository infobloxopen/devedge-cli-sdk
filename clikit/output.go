package clikit

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// Format is an output encoding selected by the global --output/-o flag.
type Format string

const (
	// FormatTable renders a human-readable aligned table (the default).
	FormatTable Format = "table"
	// FormatJSON renders indented JSON.
	FormatJSON Format = "json"
	// FormatYAML renders YAML.
	FormatYAML Format = "yaml"
)

// ParseFormat validates a --output value.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatTable, FormatJSON, FormatYAML:
		return Format(s), nil
	default:
		return "", fmt.Errorf("invalid output format %q (want table, json, or yaml)", s)
	}
}

// Printer renders command results in the selected [Format]. It is obtained from
// a [Runtime] so generated commands need not know the output plumbing.
type Printer struct {
	Out    io.Writer
	Format Format
}

// Print renders v. For table output, a slice of structs/maps renders as rows
// with a header; a single struct/map renders as a two-column KEY/VALUE table.
// JSON and YAML encode v directly.
func (p *Printer) Print(v any) error {
	switch p.Format {
	case FormatJSON:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(p.Out, string(b))
		return err
	case FormatYAML:
		b, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = p.Out.Write(b)
		return err
	default:
		return p.printTable(v)
	}
}

func (p *Printer) printTable(v any) error {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			_, err := fmt.Fprintln(p.Out, "(none)")
			return err
		}
		rv = rv.Elem()
	}
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		return p.printRows(rv)
	}
	return p.printSingle(rv)
}

// printRows renders a slice/array as a header + one row per element.
func (p *Printer) printRows(rv reflect.Value) error {
	tw := tabwriter.NewWriter(p.Out, 0, 2, 2, ' ', 0)
	defer tw.Flush()
	if rv.Len() == 0 {
		_, err := fmt.Fprintln(tw, "(no items)")
		return err
	}
	cols := columnsOf(rv.Index(0))
	fmt.Fprintln(tw, strings.Join(upperAll(cols), "\t"))
	for i := 0; i < rv.Len(); i++ {
		row := rowValues(rv.Index(i), cols)
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return nil
}

// printSingle renders a struct/map as a KEY/VALUE table.
func (p *Printer) printSingle(rv reflect.Value) error {
	tw := tabwriter.NewWriter(p.Out, 0, 2, 2, ' ', 0)
	defer tw.Flush()
	fmt.Fprintln(tw, "FIELD\tVALUE")
	for _, k := range columnsOf(rv) {
		fmt.Fprintf(tw, "%s\t%s\n", k, cellValue(fieldByKey(rv, k)))
	}
	return nil
}

// columnsOf returns the ordered column keys for a struct (json field names) or
// map (sorted keys).
func columnsOf(rv reflect.Value) []string {
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		var cols []string
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			cols = append(cols, jsonName(f))
		}
		return cols
	case reflect.Map:
		var cols []string
		for _, k := range rv.MapKeys() {
			cols = append(cols, fmt.Sprint(k.Interface()))
		}
		sort.Strings(cols)
		return cols
	default:
		return []string{"value"}
	}
}

func rowValues(rv reflect.Value, cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = cellValue(fieldByKey(rv, c))
	}
	return out
}

// fieldByKey resolves a column key against a struct (by json name) or map.
func fieldByKey(rv reflect.Value, key string) reflect.Value {
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return reflect.Value{}
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).IsExported() && jsonName(t.Field(i)) == key {
				return rv.Field(i)
			}
		}
	case reflect.Map:
		return rv.MapIndex(reflect.ValueOf(key))
	default:
		return rv
	}
	return reflect.Value{}
}

func cellValue(v reflect.Value) string {
	if !v.IsValid() {
		return ""
	}
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		b, err := json.Marshal(v.Interface())
		if err != nil {
			return fmt.Sprint(v.Interface())
		}
		return string(b)
	default:
		return fmt.Sprint(v.Interface())
	}
}

// jsonName returns a struct field's JSON key, honoring the `json` tag.
func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name
	}
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	if tag == "" || tag == "-" {
		return f.Name
	}
	return tag
}

func upperAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToUpper(s)
	}
	return out
}
