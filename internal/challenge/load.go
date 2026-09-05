package challenge

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Load reads dir/challenge.yaml, validates it, and checks that the challenge
// directory holds everything the judge will need. The returned Spec knows its
// directory, so callers can reach the workspace, tests and solution through it.
func Load(dir string) (*Spec, error) {
	path := filepath.Join(dir, SpecFile)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read challenge spec: %w", err)
	}

	spec, err := parseSpec(data, path)
	if err != nil {
		return nil, err
	}
	spec.dir = dir

	var p problems
	checkLayout(spec, &p)
	if err := p.err(path); err != nil {
		return nil, err
	}
	return spec, nil
}

// LoadAll loads every challenge directory directly under root, in key order.
//
// It refuses a root that holds a directory without a spec — that is almost
// always a misnamed challenge.yaml — and refuses two challenges claiming the
// same (id, version), because a change to a challenge must come with a new
// version.
func LoadAll(root string) ([]*Spec, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read challenge root: %w", err)
	}

	specs := make([]*Spec, 0, len(entries))
	seen := make(map[Key]string, len(entries))

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())

		spec, err := Load(dir)
		if err != nil {
			return nil, err
		}
		if first, dup := seen[spec.Key()]; dup {
			return nil, &DuplicateKeyError{Key: spec.Key(), Paths: [2]string{first, dir}}
		}
		seen[spec.Key()] = dir
		specs = append(specs, spec)
	}

	sort.Slice(specs, func(i, j int) bool {
		if specs[i].ID != specs[j].ID {
			return specs[i].ID < specs[j].ID
		}
		return specs[i].Version < specs[j].Version
	})
	return specs, nil
}

// checkLayout records a problem for every part of the challenge directory the
// judge needs and cannot find. All four are checked so an author sees the full
// list at once.
func checkLayout(s *Spec, p *problems) {
	required := []struct {
		field string
		path  string
	}{
		{"image.context", s.ImageContextDir()},
		{workspaceDir + "/", s.WorkspaceDir()},
		{testsDir + "/", s.TestsDir()},
		{solutionDir + "/", s.SolutionDir()},
	}

	for _, r := range required {
		info, err := os.Stat(r.path)
		switch {
		case os.IsNotExist(err):
			p.add(r.field, "%s is missing", filepath.Base(r.path))
		case err != nil:
			p.add(r.field, "%v", err)
		case !info.IsDir():
			p.add(r.field, "%s is not a directory", filepath.Base(r.path))
		}
	}
}
