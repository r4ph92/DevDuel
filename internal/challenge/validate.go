package challenge

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Defaults for the fields an author can leave out. Everything that is
// inherently per-challenge — id, version, category, difficulty, duration, the
// image tag, the commands, the port, the requirements — has no default and
// must be written down.
const (
	defaultImageContext  = "image"
	defaultWorkdir       = "/app"
	defaultHealthPath    = "/health"
	defaultTesterTimeout = 60 * time.Second
	defaultCPUs          = 1.0
	defaultMemoryMB      = 512
	defaultPIDs          = 128
	defaultWeight        = 1
)

// Bounds. The lower ones catch unit slips ("45s" where "45m" was meant); the
// upper ones keep one challenge from being authored into a resource ceiling
// the judge host cannot honour.
const (
	minDuration      = 1 * time.Minute
	maxDuration      = 6 * time.Hour
	maxTesterTimeout = 10 * time.Minute
	maxCPUs          = 8.0
	minMemoryMB      = 64
	maxMemoryMB      = 8192
	minPIDs          = 16
	maxPIDs          = 4096
	maxIDLen         = 64
	maxTitleLen      = 120
)

// slugRE matches the lowercase, hyphen-separated form used for challenge ids
// and requirement keys. Both end up in URLs, image tags and tester output.
var slugRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// floatingTags are tags that can be repointed at different content, which
// would break the promise that an old match can be replayed exactly.
var floatingTags = map[string]bool{"latest": true, "main": true, "master": true, "edge": true}

// resolve turns a decoded spec into a Spec, filling in every default. It is
// the only place a default is applied, so the list above is the whole story.
func resolve(raw *rawSpec, p *problems) *Spec {
	s := &Spec{
		ID:         raw.ID,
		Version:    raw.Version,
		Category:   raw.Category,
		Difficulty: raw.Difficulty,
		Duration:   duration(p, "duration", raw.Duration, 0),
		Image:      raw.Image,
		App:        raw.App,
		Tester: Tester{
			Command: raw.Tester.Command,
			Timeout: duration(p, "tester.timeout", raw.Tester.Timeout, defaultTesterTimeout),
		},
		Limits: Limits{
			CPUs:     orDefault(raw.Limits.CPUs, defaultCPUs),
			MemoryMB: orDefault(raw.Limits.MemoryMB, defaultMemoryMB),
			PIDs:     orDefault(raw.Limits.PIDs, defaultPIDs),
		},
		Requirements: make([]Requirement, len(raw.Requirements)),
	}

	if s.Image.Context == "" {
		s.Image.Context = defaultImageContext
	}
	if s.App.Workdir == "" {
		s.App.Workdir = defaultWorkdir
	}
	if s.App.HealthPath == "" {
		s.App.HealthPath = defaultHealthPath
	}

	for i, r := range raw.Requirements {
		s.Requirements[i] = Requirement{
			Key:         r.Key,
			Title:       r.Title,
			Description: r.Description,
			Weight:      orDefault(r.Weight, defaultWeight),
			Broken:      r.Broken,
		}
	}
	return s
}

// orDefault returns *v, or def when the field was left out of the spec.
func orDefault[T any](v *T, def T) T {
	if v == nil {
		return def
	}
	return *v
}

func validate(s *Spec, p *problems) {
	validateIdentity(s, p)
	validateImage(s, p)
	validateApp(s, p)
	validateTester(s, p)
	validateLimits(s, p)
	validateRequirements(s, p)
}

func validateIdentity(s *Spec, p *problems) {
	switch {
	case s.ID == "":
		p.add("id", "is required")
	case len(s.ID) > maxIDLen:
		p.add("id", "is %d characters, the limit is %d", len(s.ID), maxIDLen)
	case !slugRE.MatchString(s.ID):
		p.add("id", "%q must be lowercase letters, digits and single hyphens", s.ID)
	}

	if s.Version < 1 {
		p.add("version", "must be 1 or greater, got %d", s.Version)
	}

	switch s.Category {
	case CategoryDebugging, CategoryBuild:
	case "":
		p.add("category", "is required, one of %s", oneOf(CategoryDebugging, CategoryBuild))
	default:
		p.add("category", "%q is not a category; use one of %s", s.Category, oneOf(CategoryDebugging, CategoryBuild))
	}

	switch s.Difficulty {
	case DifficultyEasy, DifficultyMedium, DifficultyHard:
	case "":
		p.add("difficulty", "is required, one of %s", oneOf(DifficultyEasy, DifficultyMedium, DifficultyHard))
	default:
		p.add("difficulty", "%q is not a difficulty; use one of %s", s.Difficulty, oneOf(DifficultyEasy, DifficultyMedium, DifficultyHard))
	}

	if !p.has("duration") {
		switch {
		case s.Duration == 0:
			p.add("duration", "is required, for example \"45m\"")
		case s.Duration < minDuration:
			p.add("duration", "is %s, shorter than the %s minimum; check the unit", s.Duration, minDuration)
		case s.Duration > maxDuration:
			p.add("duration", "is %s, longer than the %s maximum", s.Duration, maxDuration)
		}
	}
}

func validateImage(s *Spec, p *problems) {
	switch tag := s.Image.Tag; {
	case tag == "":
		p.add("image.tag", "is required")
	case strings.ContainsAny(tag, " \t"):
		p.add("image.tag", "%q must not contain whitespace", tag)
	default:
		name, version, ok := splitImageTag(tag)
		switch {
		case !ok || version == "":
			p.add("image.tag", "%q must pin an explicit tag, for example %q", tag, name+":1")
		case floatingTags[version]:
			p.add("image.tag", "%q is a floating tag; challenges are immutable, so pin a version", tag)
		}
	}

	if !isContainedRelativePath(s.Image.Context) {
		p.add("image.context", "%q must be a relative path inside the challenge directory", s.Image.Context)
	}
}

func validateApp(s *Spec, p *problems) {
	validateCommand(p, "app.command", s.App.Command)

	if !filepath.IsAbs(s.App.Workdir) {
		p.add("app.workdir", "%q must be an absolute path", s.App.Workdir)
	}
	if s.App.Port < 1 || s.App.Port > 65535 {
		p.add("app.port", "must be between 1 and 65535, got %d", s.App.Port)
	}
	if !strings.HasPrefix(s.App.HealthPath, "/") {
		p.add("app.health_path", "%q must start with \"/\"", s.App.HealthPath)
	}
}

func validateTester(s *Spec, p *problems) {
	validateCommand(p, "tester.command", s.Tester.Command)

	if p.has("tester.timeout") {
		return
	}
	switch t := s.Tester.Timeout; {
	case t <= 0:
		p.add("tester.timeout", "must be positive, got %s", t)
	case t > maxTesterTimeout:
		p.add("tester.timeout", "is %s, longer than the %s maximum", t, maxTesterTimeout)
	}
}

func validateCommand(p *problems, field string, cmd []string) {
	if len(cmd) == 0 {
		p.add(field, "is required, for example [\"npm\", \"start\"]")
		return
	}
	for i, arg := range cmd {
		if strings.TrimSpace(arg) == "" {
			p.add(fmt.Sprintf("%s[%d]", field, i), "is empty")
		}
	}
}

func validateLimits(s *Spec, p *problems) {
	switch c := s.Limits.CPUs; {
	case c <= 0:
		p.add("limits.cpus", "must be positive, got %v", c)
	case c > maxCPUs:
		p.add("limits.cpus", "is %v, above the %v maximum", c, maxCPUs)
	}
	switch m := s.Limits.MemoryMB; {
	case m < minMemoryMB:
		p.add("limits.memory_mb", "is %d, below the %d minimum", m, minMemoryMB)
	case m > maxMemoryMB:
		p.add("limits.memory_mb", "is %d, above the %d maximum", m, maxMemoryMB)
	}
	switch pids := s.Limits.PIDs; {
	case pids < minPIDs:
		p.add("limits.pids", "is %d, below the %d minimum", pids, minPIDs)
	case pids > maxPIDs:
		p.add("limits.pids", "is %d, above the %d maximum", pids, maxPIDs)
	}
}

func validateRequirements(s *Spec, p *problems) {
	if len(s.Requirements) == 0 {
		p.add("requirements", "at least one requirement is required")
		return
	}

	seen := make(map[string]int, len(s.Requirements))
	broken := 0

	for i, r := range s.Requirements {
		field := func(name string) string { return fmt.Sprintf("requirements[%d].%s", i, name) }

		switch {
		case r.Key == "":
			p.add(field("key"), "is required")
		case !slugRE.MatchString(r.Key):
			p.add(field("key"), "%q must be lowercase letters, digits and single hyphens", r.Key)
		default:
			if first, dup := seen[r.Key]; dup {
				p.add(field("key"), "%q is already used by requirements[%d]", r.Key, first)
			} else {
				seen[r.Key] = i
			}
		}

		switch {
		case strings.TrimSpace(r.Title) == "":
			p.add(field("title"), "is required")
		case len(r.Title) > maxTitleLen:
			p.add(field("title"), "is %d characters, the limit is %d", len(r.Title), maxTitleLen)
		}

		if strings.TrimSpace(r.Description) == "" {
			p.add(field("description"), "is required; it is what the player reads")
		}
		if r.Weight <= 0 {
			p.add(field("weight"), "must be positive, got %d", r.Weight)
		}
		if r.Broken {
			broken++
		}
	}

	// With nothing declared broken there is no failure for the starting
	// workspace to reproduce, so `devduelctl challenge verify` would pass
	// vacuously and the challenge would be scored as already solved.
	if broken == 0 {
		p.add("requirements", "at least one requirement must be marked broken")
	}
}

// splitImageTag splits a reference into its name and tag, ignoring a colon
// that belongs to a registry port ("localhost:5000/app").
func splitImageTag(ref string) (name, tag string, ok bool) {
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i+1:], "/") {
		return ref, "", false
	}
	return ref[:i], ref[i+1:], true
}

// isContainedRelativePath reports whether p stays inside the directory it is
// resolved against.
func isContainedRelativePath(p string) bool {
	if p == "" || filepath.IsAbs(p) {
		return false
	}
	clean := filepath.Clean(p)
	return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func oneOf[T ~string](values ...T) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", string(v))
	}
	return strings.Join(quoted, ", ")
}
