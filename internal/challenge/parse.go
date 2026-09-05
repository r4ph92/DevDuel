package challenge

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"

	"gopkg.in/yaml.v3"
)

// rawSpec mirrors Spec as it is written in challenge.yaml. Durations arrive as
// text, so an unparseable one is reported against its field name during
// validation instead of surfacing as a YAML type error.
type rawSpec struct {
	ID           string           `yaml:"id"`
	Version      int              `yaml:"version"`
	Category     Category         `yaml:"category"`
	Difficulty   Difficulty       `yaml:"difficulty"`
	Duration     rawDuration      `yaml:"duration"`
	Image        Image            `yaml:"image"`
	App          App              `yaml:"app"`
	Tester       rawTester        `yaml:"tester"`
	Limits       rawLimits        `yaml:"limits"`
	Requirements []rawRequirement `yaml:"requirements"`
}

type rawTester struct {
	Command []string    `yaml:"command"`
	Timeout rawDuration `yaml:"timeout"`
}

// Numbers that have a default are decoded through pointers so that an
// explicit zero — "weight: 0" — is a value to reject, not an omission to
// quietly fill in.
type rawLimits struct {
	CPUs     *float64 `yaml:"cpus"`
	MemoryMB *int     `yaml:"memory_mb"`
	PIDs     *int     `yaml:"pids"`
}

type rawRequirement struct {
	Key         string `yaml:"key"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Weight      *int   `yaml:"weight"`
	Broken      bool   `yaml:"broken"`
}

// rawDuration is a duration exactly as written, before parsing.
type rawDuration string

// UnmarshalYAML accepts any scalar so that "45", "45x" and "45m" all reach
// validation, which knows the field name to blame.
func (d *rawDuration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: duration must be a scalar such as \"45m\"", node.Line)
	}
	*d = rawDuration(node.Value)
	return nil
}

// parse decodes exactly one YAML document, rejecting unknown fields so a typo
// in challenge.yaml fails loudly rather than being silently ignored.
func parse(data []byte) (*rawSpec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var raw rawSpec
	switch err := dec.Decode(&raw); {
	case errors.Is(err, io.EOF):
		return nil, errors.New("challenge spec is empty")
	case err != nil:
		return nil, err
	}

	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("challenge spec must contain exactly one YAML document")
	}
	return &raw, nil
}

// Parse decodes and validates a challenge spec without touching the
// filesystem. The Spec it returns has no directory, so the layout checks that
// [Load] performs are skipped — use it for specs that are not on disk yet.
func Parse(data []byte) (*Spec, error) {
	return parseSpec(data, "")
}

// parseSpec decodes, defaults and validates. path names the source file in
// error messages when the spec came from disk.
func parseSpec(data []byte, path string) (*Spec, error) {
	raw, err := parse(data)
	if err != nil {
		if path != "" {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return nil, err
	}

	var p problems
	spec := resolve(raw, &p)
	validate(spec, &p)

	if err := p.err(path); err != nil {
		return nil, err
	}
	return spec, nil
}

// duration parses a written duration, falling back to def when the field was
// omitted. A malformed value is recorded against field and reported as zero.
func duration(p *problems, field string, raw rawDuration, def time.Duration) time.Duration {
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(string(raw))
	if err != nil {
		p.add(field, "%q is not a duration; write it like \"45m\" or \"90s\"", string(raw))
		return 0
	}
	return d
}
