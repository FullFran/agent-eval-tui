// Package harness runs a coding agent against tasks and records whether each
// run passed its acceptance check.
package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Spec is the set of cases and variants to run, and how many times each pair
// runs.
type Spec struct {
	Runs     int       `json:"runs"`
	Cases    []Case    `json:"cases"`
	Variants []Variant `json:"variants"`
}

// Case is a task: the directory to work in, the prompt, and the shell command
// that decides whether the task was done.
type Case struct {
	ID      string `json:"id"`
	Workdir string `json:"workdir"`
	Prompt  string `json:"prompt"`
	Accept  string `json:"accept"`
}

// Variant is how the agent is launched. Every "{prompt}" in Command is
// replaced by the case prompt.
type Variant struct {
	ID      string            `json:"id"`
	Command []string          `json:"command"`
	Env     map[string]string `json:"env,omitempty"`
}

// Ids end up in run ids, OpenTelemetry resource attributes and file names, so
// they are kept to characters that need no quoting in any of them.
var validID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// LoadSpec reads and validates the spec at path. A relative workdir is
// resolved against the spec file's directory, so a spec and its cases can be
// moved together.
func LoadSpec(path string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("read spec: %w", err)
	}

	var spec Spec
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("decode spec %s: %w", path, err)
	}

	base := filepath.Dir(path)
	for i, c := range spec.Cases {
		w := c.Workdir
		if !filepath.IsAbs(w) {
			w = filepath.Join(base, w)
		}
		abs, err := filepath.Abs(w)
		if err != nil {
			return Spec{}, fmt.Errorf("case %q: resolve workdir: %w", c.ID, err)
		}
		spec.Cases[i].Workdir = abs
	}

	if err := spec.validate(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func (s Spec) validate() error {
	if s.Runs < 1 {
		return errors.New("runs must be at least 1")
	}
	if len(s.Cases) == 0 {
		return errors.New("at least one case is required")
	}
	if len(s.Variants) == 0 {
		return errors.New("at least one variant is required")
	}

	seen := map[string]bool{}
	for _, c := range s.Cases {
		if !validID.MatchString(c.ID) {
			return fmt.Errorf("case id %q is invalid: use letters, digits, '.', '_' or '-'", c.ID)
		}
		if seen[c.ID] {
			return fmt.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
		if fi, err := os.Stat(c.Workdir); err != nil || !fi.IsDir() {
			return fmt.Errorf("case %q: workdir %q is not a directory", c.ID, c.Workdir)
		}
		if strings.TrimSpace(c.Accept) == "" {
			return fmt.Errorf("case %q: accept command is empty", c.ID)
		}
	}

	seen = map[string]bool{}
	for _, v := range s.Variants {
		if !validID.MatchString(v.ID) {
			return fmt.Errorf("variant id %q is invalid: use letters, digits, '.', '_' or '-'", v.ID)
		}
		if seen[v.ID] {
			return fmt.Errorf("duplicate variant id %q", v.ID)
		}
		seen[v.ID] = true
		if len(v.Command) == 0 {
			return fmt.Errorf("variant %q: command is empty", v.ID)
		}
	}
	return nil
}
