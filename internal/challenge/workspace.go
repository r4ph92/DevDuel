package challenge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// File is one file of a challenge's starting workspace, as it will be handed
// to both players.
type File struct {
	// Path is relative to the workspace root, always with forward slashes,
	// because it becomes a row that a Linux container will later see.
	Path string
	// Content is the bytes, unmodified. Text and binary are not
	// distinguished: whatever the author committed is what the player gets.
	Content []byte
}

// Limits on a starting workspace, checked when it is read rather than when a
// match tries to use it.
//
// These are deliberately well under anything that would trouble Postgres. A
// challenge is a repository somebody is meant to read inside an hour, so a
// workspace that trips one of these is a mistake in the challenge, and
// finding that out at registration beats finding it out mid-match.
const (
	// MaxFileBytes matches the ceiling the schema puts on a single file.
	MaxFileBytes = 1 << 20
	// MaxWorkspaceFiles is how many files one workspace may hold.
	MaxWorkspaceFiles = 200
	// MaxWorkspaceBytes is the total across those files.
	MaxWorkspaceBytes = 5 << 20
)

// Workspace reads the challenge's starting workspace from disk.
func (s *Spec) Workspace() ([]File, error) {
	dir := s.WorkspaceDir()
	if dir == "" {
		return nil, fmt.Errorf("challenge %s was parsed without a directory, so it has no workspace", s.Key())
	}
	return LoadWorkspace(dir)
}

// LoadWorkspace reads every file under dir into memory, in path order.
//
// Symlinks are refused rather than followed. A link is how a starting
// workspace would otherwise reach a file outside itself, and the whole point
// of this directory is that it holds exactly what the player begins with.
func LoadWorkspace(dir string) ([]File, error) {
	var files []File
	var total int

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		// The type bits rather than a stat: WalkDir does not follow links, so
		// a link is reported as one here instead of as the file it points at.
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("workspace contains a symlink: %s", path)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("workspace contains something that is not a regular file: %s", path)
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("resolve %s inside the workspace: %w", path, err)
		}

		file := File{Path: filepath.ToSlash(rel)}
		// Checked here so that an unusable path is a challenge that fails to
		// register, rather than a constraint violation during a match.
		if err := file.validPath(); err != nil {
			return err
		}

		file.Content, err = os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(file.Content) > MaxFileBytes {
			return fmt.Errorf("%s is %d bytes, over the %d byte limit for one file",
				file.Path, len(file.Content), MaxFileBytes)
		}

		total += len(file.Content)
		if total > MaxWorkspaceBytes {
			return fmt.Errorf("workspace is larger than the %d byte limit", MaxWorkspaceBytes)
		}
		if len(files) == MaxWorkspaceFiles {
			return fmt.Errorf("workspace holds more than %d files", MaxWorkspaceFiles)
		}

		files = append(files, file)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read workspace %s: %w", dir, err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("read workspace %s: it has no files", dir)
	}

	// Sorted, so that reading the same workspace twice gives the same order.
	// Registration compares two lists, and a comparison of two lists is only
	// meaningful if neither depends on the order the filesystem walked in.
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// Digest is a fingerprint of the file's bytes. Registration compares these
// against digests computed inside Postgres, so that checking whether a
// registered workspace still matches the one on disk never has to carry the
// contents back out of the database.
func (f File) Digest() string {
	sum := sha256.Sum256(f.Content)
	return hex.EncodeToString(sum[:])
}

// validPath rejects a path the workspace tables would refuse, and says why.
// The schema enforces the same rule; this exists so the complaint names the
// challenge file rather than arriving as a constraint violation.
func (f File) validPath() error {
	switch {
	case f.Path == "":
		return fmt.Errorf("a workspace file has an empty path")
	case len(f.Path) > 512:
		return fmt.Errorf("%s is longer than 512 characters", f.Path)
	case strings.HasPrefix(f.Path, "/"), strings.HasSuffix(f.Path, "/"), strings.Contains(f.Path, "//"):
		return fmt.Errorf("%s is not a normalised relative path", f.Path)
	}
	for part := range strings.SplitSeq(f.Path, "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("%s climbs outside the workspace", f.Path)
		}
	}
	return nil
}
