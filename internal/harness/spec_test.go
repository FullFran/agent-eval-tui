package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSpec(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSpecResolvesRelativeWorkdir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cases", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeSpec(t, dir, `{
		"runs": 2,
		"cases": [{"id": "x", "workdir": "cases/x", "prompt": "do it", "accept": "true"}],
		"variants": [{"id": "base", "command": ["agent", "{prompt}"], "env": {"A": "1"}}]
	}`)

	spec, err := LoadSpec(path)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	want := filepath.Join(dir, "cases", "x")
	if got := spec.Cases[0].Workdir; got != want || !filepath.IsAbs(got) {
		t.Errorf("workdir = %q, want %q", got, want)
	}
	if spec.Runs != 2 || spec.Cases[0].Prompt != "do it" || spec.Variants[0].Env["A"] != "1" {
		t.Errorf("unexpected spec: %+v", spec)
	}
}

func TestLoadSpecValidation(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "w"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	okCase := `{"id": "c", "workdir": "w", "prompt": "p", "accept": "true"}`
	okVariant := `{"id": "v", "command": ["a"]}`
	missing := filepath.Join(dir, "missing")
	file := filepath.Join(dir, "file")

	tests := []struct {
		name, body, want string
	}{
		{"runs", `{"runs": 0, "cases": [` + okCase + `], "variants": [` + okVariant + `]}`,
			"runs must be at least 1"},
		{"no cases", `{"runs": 1, "cases": [], "variants": [` + okVariant + `]}`,
			"at least one case is required"},
		{"no variants", `{"runs": 1, "cases": [` + okCase + `], "variants": []}`,
			"at least one variant is required"},
		{"empty case id", `{"runs": 1, "cases": [{"id": "", "workdir": "w", "accept": "true"}], "variants": [` + okVariant + `]}`,
			`case id "" is invalid: use letters, digits, '.', '_' or '-'`},
		{"bad case id", `{"runs": 1, "cases": [{"id": "a b", "workdir": "w", "accept": "true"}], "variants": [` + okVariant + `]}`,
			`case id "a b" is invalid: use letters, digits, '.', '_' or '-'`},
		{"bad variant id", `{"runs": 1, "cases": [` + okCase + `], "variants": [{"id": "x/y", "command": ["a"]}]}`,
			`variant id "x/y" is invalid: use letters, digits, '.', '_' or '-'`},
		{"duplicate case", `{"runs": 1, "cases": [` + okCase + `,` + okCase + `], "variants": [` + okVariant + `]}`,
			`duplicate case id "c"`},
		{"duplicate variant", `{"runs": 1, "cases": [` + okCase + `], "variants": [` + okVariant + `,` + okVariant + `]}`,
			`duplicate variant id "v"`},
		{"missing workdir", `{"runs": 1, "cases": [{"id": "c", "workdir": "missing", "accept": "true"}], "variants": [` + okVariant + `]}`,
			`case "c": workdir "` + missing + `" is not a directory`},
		{"file workdir", `{"runs": 1, "cases": [{"id": "c", "workdir": "file", "accept": "true"}], "variants": [` + okVariant + `]}`,
			`case "c": workdir "` + file + `" is not a directory`},
		{"empty accept", `{"runs": 1, "cases": [{"id": "c", "workdir": "w", "accept": "  "}], "variants": [` + okVariant + `]}`,
			`case "c": accept command is empty`},
		{"empty command", `{"runs": 1, "cases": [` + okCase + `], "variants": [{"id": "v", "command": []}]}`,
			`variant "v": command is empty`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadSpec(writeSpec(t, dir, tt.body))
			if err == nil || err.Error() != tt.want {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadSpecRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := writeSpec(t, dir, `{"runs": 1, "extra": true}`)
	if _, err := LoadSpec(path); err == nil {
		t.Fatal("want error for unknown field")
	}
}
