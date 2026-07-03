// Package gen turns an enriched OpenAPI v3 spec (native required/readOnly/
// writeOnly/enum plus x-aip-* extensions) into a cobra "domain command module"
// that imports the clikit runtime. It is the CLI-side analog of the apx "go"
// client generator: parse the contract, emit typed surface, import the runtime.
//
// The logic here is exec-free and unit-testable: [Parse] yields a [Model] and
// [Render] turns it into Go source.
package gen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Field is one resource field projected to a CLI flag, honoring field behavior.
type Field struct {
	JSONName    string   // wire name, e.g. "displayName"
	GoName      string   // exported Go name, e.g. "DisplayName"
	FlagName    string   // kebab flag name, e.g. "display-name"
	GoType      string   // "string", "int", "float64", "bool", "[]string"
	FlagKind    string   // "string","int","float","bool","stringslice","secret"
	Description string   // one-line help
	Required    bool     // client-REQUIRED → required flag on create
	OutputOnly  bool     // OUTPUT_ONLY/readOnly → never an input flag
	InputOnly   bool     // INPUT_ONLY/secret/writeOnly → read from stdin, not echoed
	Immutable   bool     // IMMUTABLE → allowed at create, excluded from update
	Reference   string   // x-aip-references target type, if any
	Enum        []string // allowed values, if a string enum
}

// Method is a standard AIP method present for a resource.
type Method struct {
	Kind       string // Get, List, Create, Update, Delete
	HTTPMethod string // GET, POST, PATCH, DELETE
}

// Resource is one AIP resource (x-aip-resource) and its methods/fields.
type Resource struct {
	GoName         string // "Widget"
	Collection     string // "widgets"
	CollectionPath string // "/v1/widgets"
	ResourceType   string // "toy.example.com/Widget"
	Key            string // "id"
	Fields         []Field
	Methods        map[string]Method // by kind

	// List projection (AIP: the repeated field is named after the collection).
	ListItemsJSON  string // "widgets"
	ListItemsGo    string // "Widgets"
	NextTokenJSON  string // "nextPageToken"
	NextTokenGo    string // "NextPageToken"
	PageSizeParam  string // "pageSize"
	PageTokenParam string // "pageToken"
}

// Has reports whether a standard method kind is present.
func (r Resource) Has(kind string) bool { _, ok := r.Methods[kind]; return ok }

// Model is the full input to rendering.
type Model struct {
	Module    string // go module path for generated imports / go.mod
	Package   string // generated Go package name
	Domain    string // domain command name (e.g. "widgets")
	App       string // rebranded app name (e.g. "ib")
	Resources []Resource
}

type aipResource struct {
	Type    string   `json:"type"`
	Key     string   `json:"key"`
	Pattern []string `json:"pattern"`
}

type aipPagination struct {
	PageSizeParam      string `json:"pageSizeParam"`
	PageTokenParam     string `json:"pageTokenParam"`
	NextPageTokenField string `json:"nextPageTokenField"`
}

type aipReferences struct {
	Type string `json:"type"`
}

// standardKinds is the fixed emit order for verbs.
var standardKinds = []string{"Get", "List", "Create", "Update", "Delete"}

// Parse loads an enriched OpenAPI v3 document and builds a [Model]. It fails
// loud when the spec has no x-aip-resource — cligen has nothing to generate.
func Parse(specData []byte, module, pkg, domain, app string) (*Model, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(specData)
	if err != nil {
		return nil, fmt.Errorf("load spec: %w", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		return nil, fmt.Errorf("spec has no component schemas")
	}

	ops := collectOperations(doc)

	var resources []Resource
	for name, ref := range doc.Components.Schemas {
		if ref == nil || ref.Value == nil {
			continue
		}
		var res aipResource
		if !decodeExt(ref.Value.Extensions, "x-aip-resource", &res) {
			continue
		}
		r, err := buildResource(name, ref.Value, res, ops)
		if err != nil {
			return nil, err
		}
		resources = append(resources, r)
	}
	if len(resources) == 0 {
		return nil, fmt.Errorf("spec has no x-aip-resource schemas; nothing to generate")
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].GoName < resources[j].GoName })

	return &Model{Module: module, Package: pkg, Domain: domain, App: app, Resources: resources}, nil
}

type opInfo struct {
	kind        string
	httpMethod  string
	path        string
	pagination  aipPagination
	hasPageInfo bool
}

func collectOperations(doc *openapi3.T) []opInfo {
	var out []opInfo
	if doc.Paths == nil {
		return out
	}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op == nil {
				continue
			}
			kind, _ := op.Extensions["x-aip-method"].(string)
			if kind == "" {
				continue
			}
			oi := opInfo{kind: kind, httpMethod: strings.ToUpper(method), path: path}
			if decodeExt(op.Extensions, "x-aip-pagination", &oi.pagination) {
				oi.hasPageInfo = true
			}
			out = append(out, oi)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func buildResource(schemaName string, schema *openapi3.Schema, res aipResource, ops []opInfo) (Resource, error) {
	r := Resource{
		GoName:       goNameFromType(res.Type, schemaName),
		Collection:   collectionFromPattern(res.Pattern, res.Type),
		ResourceType: res.Type,
		Key:          res.Key,
		Methods:      map[string]Method{},
	}
	if r.Key == "" {
		r.Key = "id"
	}

	// Fields, sorted by wire name for deterministic output.
	var names []string
	for pn := range schema.Properties {
		names = append(names, pn)
	}
	sort.Strings(names)
	requiredSet := map[string]bool{}
	for _, req := range schema.Required {
		requiredSet[req] = true
	}
	for _, pn := range names {
		pref := schema.Properties[pn]
		if pref == nil || pref.Value == nil {
			continue
		}
		r.Fields = append(r.Fields, buildField(pn, pref.Value, requiredSet[pn]))
	}

	// Methods for this resource, matched by collection segment in the path.
	for _, oi := range ops {
		if !isStandardKind(oi.kind) || !pathHasSegment(oi.path, r.Collection) {
			continue
		}
		r.Methods[oi.kind] = Method{Kind: oi.kind, HTTPMethod: oi.httpMethod}
		switch oi.kind {
		case "List":
			r.CollectionPath = oi.path
			if oi.hasPageInfo {
				r.PageSizeParam = oi.pagination.PageSizeParam
				r.PageTokenParam = oi.pagination.PageTokenParam
				r.NextTokenJSON = oi.pagination.NextPageTokenField
			}
		case "Create":
			if r.CollectionPath == "" {
				r.CollectionPath = oi.path
			}
		}
	}
	if r.CollectionPath == "" {
		return r, fmt.Errorf("resource %s has no List/Create path to derive its collection URL", r.GoName)
	}

	// Pagination + list-projection defaults (AIP conventions).
	if r.PageSizeParam == "" {
		r.PageSizeParam = "pageSize"
	}
	if r.PageTokenParam == "" {
		r.PageTokenParam = "pageToken"
	}
	if r.NextTokenJSON == "" {
		r.NextTokenJSON = "nextPageToken"
	}
	r.NextTokenGo = pascal(r.NextTokenJSON)
	r.ListItemsJSON = r.Collection
	r.ListItemsGo = pascal(r.Collection)
	return r, nil
}

func buildField(name string, schema *openapi3.Schema, required bool) Field {
	f := Field{
		JSONName:    name,
		GoName:      pascal(name),
		FlagName:    kebab(name),
		GoType:      goType(schema),
		Description: firstLine(schema.Description),
		Required:    required,
	}
	behaviors := fieldBehaviors(schema)
	for _, b := range behaviors {
		switch b {
		case "OUTPUT_ONLY":
			f.OutputOnly = true
		case "INPUT_ONLY":
			f.InputOnly = true
		case "IMMUTABLE":
			f.Immutable = true
		case "REQUIRED":
			f.Required = true
		}
	}
	if schema.ReadOnly {
		f.OutputOnly = true
	}
	if schema.WriteOnly {
		f.InputOnly = true
	}
	var refs aipReferences
	if decodeExt(schema.Extensions, "x-aip-references", &refs) {
		f.Reference = refs.Type
	}
	for _, e := range schema.Enum {
		f.Enum = append(f.Enum, fmt.Sprint(e))
	}
	f.FlagKind = flagKind(f)
	return f
}

func fieldBehaviors(schema *openapi3.Schema) []string {
	var out []string
	if raw, ok := schema.Extensions["x-aip-field-behavior"]; ok {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func flagKind(f Field) string {
	if f.InputOnly {
		return "secret"
	}
	switch f.GoType {
	case "int":
		return "int"
	case "float64":
		return "float"
	case "bool":
		return "bool"
	case "[]string":
		return "stringslice"
	default:
		return "string"
	}
}

func goType(s *openapi3.Schema) string {
	if s == nil || s.Type == nil {
		return "string"
	}
	switch {
	case s.Type.Is("string"):
		return "string"
	case s.Type.Is("integer"):
		return "int"
	case s.Type.Is("number"):
		return "float64"
	case s.Type.Is("boolean"):
		return "bool"
	case s.Type.Is("array"):
		if s.Items != nil && s.Items.Value != nil {
			return "[]" + goType(s.Items.Value)
		}
		return "[]string"
	default:
		return "string"
	}
}

// decodeExt round-trips an OpenAPI extension value into out via JSON, tolerating
// both decoded-map and json.RawMessage representations.
func decodeExt(ext map[string]any, key string, out any) bool {
	raw, ok := ext[key]
	if !ok {
		return false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, out) == nil
}

func isStandardKind(kind string) bool {
	for _, k := range standardKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func pathHasSegment(path, seg string) bool {
	for _, s := range strings.Split(strings.Trim(path, "/"), "/") {
		if s == seg {
			return true
		}
	}
	return false
}

// goNameFromType derives an exported type name from an AIP resource type
// ("toy.example.com/Widget" → "Widget"), falling back to the schema name.
func goNameFromType(resourceType, schemaName string) string {
	if i := strings.LastIndexByte(resourceType, '/'); i >= 0 && i+1 < len(resourceType) {
		return pascal(resourceType[i+1:])
	}
	return pascal(strings.TrimPrefix(schemaName, "v1"))
}

// collectionFromPattern extracts the collection from an AIP pattern
// ("widgets/{widget}" → "widgets"), falling back to a lowercased type tail.
func collectionFromPattern(pattern []string, resourceType string) string {
	if len(pattern) > 0 {
		if i := strings.IndexByte(pattern[0], '/'); i > 0 {
			return pattern[0][:i]
		}
		return pattern[0]
	}
	if i := strings.LastIndexByte(resourceType, '/'); i >= 0 {
		return strings.ToLower(resourceType[i+1:]) + "s"
	}
	return strings.ToLower(resourceType)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// pascal converts a camelCase/snake/kebab wire name to an exported Go name.
func pascal(s string) string {
	parts := splitWords(s)
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	out := b.String()
	if out == "" {
		return "Field"
	}
	return out
}

// kebab converts a camelCase/snake wire name to a kebab flag name.
func kebab(s string) string {
	return strings.Join(splitWords(s), "-")
}

// splitWords breaks a name on separators and camelCase boundaries, lowercasing.
func splitWords(s string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	for i, r := range s {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ':
			flush()
		case r >= 'A' && r <= 'Z':
			if i > 0 {
				flush()
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return words
}
