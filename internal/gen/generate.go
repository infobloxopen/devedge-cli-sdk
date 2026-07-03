package gen

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// Options configures a generation run.
type Options struct {
	SpecPath  string // path to the enriched OpenAPI v3 spec
	OutputDir string // directory to emit the package into
	Module    string // go module path (used for a standalone go.mod / imports)
	Domain    string // domain command name
	Package   string // generated Go package name
	App       string // rebranded app name (e.g. "ib")
}

// tmplField is a create/update flag projection of a [Field].
type tmplField struct {
	JSONName   string
	FlagName   string
	VarName    string
	GoType     string
	FlagKind   string
	HelpQuoted string
	EnumList   string
	Enum       []string
	Required   bool
}

// tmplResource embeds a [Resource] and adds the rendering projections.
type tmplResource struct {
	Resource
	LowerName    string
	CreateFields []tmplField
	UpdateFields []tmplField
}

// tmplModel is the full template input.
type tmplModel struct {
	Package     string
	Domain      string
	App         string
	Module      string
	ImportPath  string
	Nested      bool
	NeedStrconv bool
	NeedSecret  bool
	NeedEnum    bool
	Resources   []tmplResource
}

// Render turns a [Model] into formatted Go source files keyed by their path
// relative to the output directory. importPath is the Go import path of the
// generated package (for the plugin main).
func Render(m *Model, importPath string, standalone bool) (map[string]string, error) {
	view := buildView(m, importPath)

	files := map[string]string{}
	for name, tmpl := range map[string]string{
		m.Package + ".go": domainTmpl,
		"doc.go":          docTmpl,
		filepath.Join("cmd", m.App+"-"+m.Domain, "main.go"): pluginMainTmpl,
	} {
		src, err := execGo(tmpl, view)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", name, err)
		}
		files[name] = src
	}
	if standalone {
		src, err := execRaw(goModTmpl, view)
		if err != nil {
			return nil, fmt.Errorf("render go.mod: %w", err)
		}
		files["go.mod"] = src
	}
	return files, nil
}

// Generate parses the spec, renders the module, and writes it under OutputDir.
// It returns the sorted list of written file paths.
func Generate(opts Options) ([]string, error) {
	if opts.SpecPath == "" || opts.OutputDir == "" {
		return nil, fmt.Errorf("SpecPath and OutputDir are required")
	}
	data, err := os.ReadFile(opts.SpecPath)
	if err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	// The spec is YAML or JSON; kin-openapi accepts both, but validate it is
	// at least parseable structured data first for a clearer error.
	var probe map[string]any
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("spec is not valid YAML/JSON: %w", err)
	}

	importPath, module, standalone := resolveModule(opts.OutputDir, opts.Module)

	model, err := Parse(data, module, opts.Package, opts.Domain, opts.App)
	if err != nil {
		return nil, err
	}

	files, err := Render(model, importPath, standalone)
	if err != nil {
		return nil, err
	}

	var written []string
	for rel, content := range files {
		dst := filepath.Join(opts.OutputDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return nil, err
		}
		written = append(written, dst)
	}
	sort.Strings(written)
	return written, nil
}

func buildView(m *Model, importPath string) tmplModel {
	view := tmplModel{
		Package:    m.Package,
		Domain:     m.Domain,
		App:        m.App,
		Module:     m.Module,
		ImportPath: importPath,
		Nested:     len(m.Resources) > 1,
	}
	for _, r := range m.Resources {
		tr := tmplResource{Resource: r, LowerName: lowerFirst(r.GoName)}
		if r.Has("List") {
			view.NeedStrconv = true
		}
		for _, f := range r.Fields {
			if f.OutputOnly {
				continue
			}
			tf := toTmplField(f)
			tr.CreateFields = append(tr.CreateFields, tf)
			if !f.Immutable {
				tr.UpdateFields = append(tr.UpdateFields, tf)
			}
			if tf.FlagKind == "secret" {
				view.NeedSecret = true
			}
			if len(tf.Enum) > 0 {
				view.NeedEnum = true
			}
		}
		view.Resources = append(view.Resources, tr)
	}
	return view
}

func toTmplField(f Field) tmplField {
	help := f.Description
	if f.FlagKind == "secret" {
		help = "read " + f.FlagName + " (write-only material) from stdin, not echoed"
	} else {
		if f.Immutable {
			help = strings.TrimSpace(help + " (immutable; set at create)")
		}
		if len(f.Enum) > 0 {
			help = strings.TrimSpace(help + " (one of: " + strings.Join(f.Enum, ", ") + ")")
		}
	}
	if help == "" {
		help = f.FlagName
	}
	return tmplField{
		JSONName:   f.JSONName,
		FlagName:   f.FlagName,
		VarName:    lowerFirst(f.GoName),
		GoType:     f.GoType,
		FlagKind:   f.FlagKind,
		Required:   f.Required,
		Enum:       f.Enum,
		EnumList:   strings.Join(f.Enum, ", "),
		HelpQuoted: strconv.Quote(help),
	}
}

// resolveModule determines the import path of the generated package. When the
// output dir sits inside an existing module it reuses that module + relative
// path; otherwise it treats moduleFlag as a standalone module rooted at the
// output dir.
func resolveModule(outputDir, moduleFlag string) (importPath, module string, standalone bool) {
	abs, err := filepath.Abs(outputDir)
	if err != nil {
		abs = outputDir
	}
	if gomodDir, mod := findModule(abs); mod != "" {
		rel, err := filepath.Rel(gomodDir, abs)
		if err != nil || rel == "." {
			return mod, mod, false
		}
		return mod + "/" + filepath.ToSlash(rel), mod, false
	}
	return moduleFlag, moduleFlag, true
}

// findModule walks up from dir to locate a go.mod and returns its directory and
// declared module path.
func findModule(dir string) (string, string) {
	for {
		gomod := filepath.Join(dir, "go.mod")
		if b, err := os.ReadFile(gomod); err == nil {
			return dir, modulePath(b)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
}

func modulePath(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

func execGo(tmpl string, data any) (string, error) {
	raw, err := execRaw(tmpl, data)
	if err != nil {
		return "", err
	}
	formatted, err := format.Source([]byte(raw))
	if err != nil {
		return "", fmt.Errorf("gofmt generated source: %w\n---\n%s", err, raw)
	}
	return string(formatted), nil
}

func execRaw(tmpl string, data any) (string, error) {
	t, err := template.New("gen").Funcs(template.FuncMap{
		"bq": func() string { return "`" },
	}).Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
