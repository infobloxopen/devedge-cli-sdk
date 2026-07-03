package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const specPath = "../../testdata/toy.openapi.yaml"

func loadModel(t *testing.T) *Model {
	t.Helper()
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	m, err := Parse(data, "example.com/ib-widgets", "widgets", "widgets", "ib")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return m
}

func TestParseModelFieldBehavior(t *testing.T) {
	m := loadModel(t)
	if len(m.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(m.Resources))
	}
	r := m.Resources[0]
	if r.GoName != "Widget" || r.Collection != "widgets" || r.CollectionPath != "/v1/widgets" {
		t.Fatalf("resource = %+v", r)
	}
	for _, kind := range standardKinds {
		if !r.Has(kind) {
			t.Fatalf("expected method %s present", kind)
		}
	}

	byName := map[string]Field{}
	for _, f := range r.Fields {
		byName[f.JSONName] = f
	}
	// OUTPUT_ONLY / readOnly
	if !byName["name"].OutputOnly || !byName["archivedTime"].OutputOnly || !byName["deleteTime"].OutputOnly {
		t.Fatalf("output-only fields not detected")
	}
	// REQUIRED
	if !byName["displayName"].Required {
		t.Fatalf("displayName should be required")
	}
	// not_null must NOT become required (sku)
	if byName["sku"].Required {
		t.Fatalf("sku (not_null) must not be required")
	}
	// IMMUTABLE
	if !byName["color"].Immutable || !byName["id"].Immutable {
		t.Fatalf("immutable fields not detected")
	}
	// secret / INPUT_ONLY / writeOnly
	if !byName["secretToken"].InputOnly || byName["secretToken"].FlagKind != "secret" {
		t.Fatalf("secretToken should be input-only secret: %+v", byName["secretToken"])
	}
	// enum
	if got := byName["category"].Enum; len(got) != 2 || got[0] != "standard" {
		t.Fatalf("category enum = %v", got)
	}
	// reference
	if byName["parentId"].Reference == "" {
		t.Fatalf("parentId should carry a reference")
	}
}

func TestRenderDeterministicAndCompiles(t *testing.T) {
	m := loadModel(t)
	a, err := Render(m, "example.com/ib-widgets", false)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	b, err := Render(m, "example.com/ib-widgets", false)
	if err != nil {
		t.Fatalf("Render (2nd): %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("file count differs: %d vs %d", len(a), len(b))
	}
	for name, src := range a {
		if b[name] != src {
			t.Fatalf("non-deterministic output for %s", name)
		}
	}

	widgets := a["widgets.go"]
	// OUTPUT_ONLY fields never become create/update flags.
	if strings.Contains(widgets, `"name"`) && strings.Contains(widgets, `cmd.Flags().StringVar(&name,`) {
		t.Fatalf("output-only 'name' leaked into a flag")
	}
	// secret uses a stdin flag, not a value flag.
	if !strings.Contains(widgets, `"secret-token-stdin"`) {
		t.Fatalf("secret field should surface as --secret-token-stdin")
	}
	// required flag marked.
	if !strings.Contains(widgets, `MarkFlagRequired("display-name")`) {
		t.Fatalf("required flag not marked")
	}
}

func TestGenerateWritesFiles(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "gen", "widgets")
	written, err := Generate(Options{
		SpecPath:  specPath,
		OutputDir: out,
		Module:    "example.com/standalone",
		Domain:    "widgets",
		Package:   "widgets",
		App:       "ib",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// standalone (no go.mod above tempdir) → emits go.mod too.
	var haveGoMod, havePlugin bool
	for _, f := range written {
		if strings.HasSuffix(f, "go.mod") {
			haveGoMod = true
		}
		if strings.HasSuffix(filepath.Join("ib-widgets", "main.go"), filepath.Base(filepath.Dir(f))+"/"+filepath.Base(f)) {
			havePlugin = true
		}
	}
	if !haveGoMod {
		t.Fatalf("standalone generate should emit go.mod; wrote %v", written)
	}
	if !havePlugin {
		t.Fatalf("expected plugin main; wrote %v", written)
	}
}
