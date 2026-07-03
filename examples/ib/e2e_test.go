package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeWidgets is a minimal in-memory implementation of the toy Widget REST API
// the generated CLI drives. It records that every request carried the bearer
// token so the e2e proves the clikit authed transport works end to end.
type fakeWidgets struct {
	mu         sync.Mutex
	store      map[string]map[string]any
	seenBearer bool
	nextID     int
}

func newFake() *fakeWidgets {
	return &fakeWidgets{store: map[string]map[string]any{
		"w1": {"id": "w1", "name": "widgets/w1", "displayName": "Widget One", "category": "standard"},
		"w2": {"id": "w2", "name": "widgets/w2", "displayName": "Widget Two", "category": "premium"},
	}}
}

func (f *fakeWidgets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") == "Bearer dev-stub-token" {
		f.seenBearer = true
	}
	w.Header().Set("Content-Type", "application/json")

	path := strings.TrimPrefix(r.URL.Path, "/v1/widgets")
	id := strings.TrimPrefix(path, "/")

	switch {
	case r.Method == http.MethodGet && path == "":
		items := make([]map[string]any, 0, len(f.store))
		for _, k := range []string{"w1", "w2"} { // deterministic order
			if v, ok := f.store[k]; ok {
				items = append(items, v)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"widgets": items, "nextPageToken": ""})
	case r.Method == http.MethodGet && id != "":
		v, ok := f.store[id]
		if !ok {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	case r.Method == http.MethodPost && path == "":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.nextID++
		newID := "new" + string(rune('0'+f.nextID))
		body["id"] = newID
		body["name"] = "widgets/" + newID
		f.store[newID] = body
		_ = json.NewEncoder(w).Encode(body)
	case r.Method == http.MethodPatch && id != "":
		cur, ok := f.store[id]
		if !ok {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		for k, v := range body {
			cur[k] = v
		}
		_ = json.NewEncoder(w).Encode(cur)
	case r.Method == http.MethodDelete && id != "":
		delete(f.store, id)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	default:
		http.Error(w, `{"message":"unhandled"}`, http.StatusBadRequest)
	}
}

// runIB drives the generated ib CLI in-process against srv, returning stdout.
func runIB(t *testing.T, srvURL string, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	app := newApp()
	app.SetOut(&buf)
	full := append(args, "--server", srvURL, "--dev")
	if err := app.ExecuteArgs(full); err != nil {
		t.Fatalf("ib %v: %v", full, err)
	}
	return buf.String()
}

func TestExampleE2E(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	// list (json): both seeded widgets present.
	out := runIB(t, srv.URL, "widgets", "list", "-o", "json")
	if !strings.Contains(out, "Widget One") || !strings.Contains(out, "w2") {
		t.Fatalf("list json missing widgets:\n%s", out)
	}

	// list (default table): header + rows.
	out = runIB(t, srv.URL, "widgets", "list")
	if !strings.Contains(strings.ToUpper(out), "DISPLAYNAME") || !strings.Contains(out, "w1") {
		t.Fatalf("list table missing header/rows:\n%s", out)
	}

	// get (json): a single widget.
	out = runIB(t, srv.URL, "widgets", "get", "w1", "-o", "json")
	if !strings.Contains(out, `"displayName": "Widget One"`) {
		t.Fatalf("get json unexpected:\n%s", out)
	}
	// AIP-122 name form is accepted too.
	out = runIB(t, srv.URL, "widgets", "get", "widgets/w2", "-o", "json")
	if !strings.Contains(out, "Widget Two") {
		t.Fatalf("get by resource name failed:\n%s", out)
	}

	// create (json): required flag honored, server assigns id.
	out = runIB(t, srv.URL, "widgets", "create", "--display-name", "Made By CLI", "--category", "standard", "-o", "json")
	if !strings.Contains(out, "Made By CLI") {
		t.Fatalf("create json unexpected:\n%s", out)
	}

	// update (json): patch a field.
	out = runIB(t, srv.URL, "widgets", "update", "w1", "--display-name", "Renamed", "-o", "json")
	if !strings.Contains(out, "Renamed") {
		t.Fatalf("update json unexpected:\n%s", out)
	}

	// delete: friendly resource-name confirmation.
	out = runIB(t, srv.URL, "widgets", "delete", "w2")
	if !strings.Contains(out, "deleted widgets/w2") {
		t.Fatalf("delete unexpected:\n%s", out)
	}

	if !fake.seenBearer {
		t.Fatal("service never saw the bearer token: authed transport did not attach it")
	}
}

func TestCreateEnumValidation(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	var buf bytes.Buffer
	app := newApp()
	app.SetOut(&buf)
	err := app.ExecuteArgs([]string{"widgets", "create", "--display-name", "X", "--category", "bogus", "--server", srv.URL, "--dev"})
	if err == nil || !strings.Contains(err.Error(), "must be one of") {
		t.Fatalf("expected enum validation error, got %v", err)
	}
}

func TestCreateRequiredFlag(t *testing.T) {
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	var buf bytes.Buffer
	app := newApp()
	app.SetOut(&buf)
	// display-name is REQUIRED; omitting it must fail before any request.
	err := app.ExecuteArgs([]string{"widgets", "create", "--category", "standard", "--server", srv.URL, "--dev"})
	if err == nil || !strings.Contains(err.Error(), "display-name") {
		t.Fatalf("expected required-flag error, got %v", err)
	}
}
