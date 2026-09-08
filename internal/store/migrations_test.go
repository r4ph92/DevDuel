package store

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

// file is one entry for a synthetic migration set.
func file(name, body string) fstest.MapFS {
	return fstest.MapFS{migrationDir + "/" + name: &fstest.MapFile{Data: []byte(body)}}
}

func merge(sets ...fstest.MapFS) fstest.MapFS {
	out := fstest.MapFS{}
	for _, s := range sets {
		for name, f := range s {
			out[name] = f
		}
	}
	return out
}

func TestLoadMigrationsOrdersByVersion(t *testing.T) {
	fsys := merge(
		file("0002_second.sql", "select 2"),
		file("0001_first.sql", "select 1"),
		file("0003_third.sql", "select 3"),
	)

	got, err := loadMigrations(fsys)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}

	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("loaded %d migrations, want %d", len(got), len(want))
	}
	for i, m := range got {
		if m.version != i+1 || m.name != want[i] {
			t.Errorf("migration %d = %d_%s, want %d_%s", i, m.version, m.name, i+1, want[i])
		}
	}
}

func TestLoadMigrationsChecksumsTheContents(t *testing.T) {
	one, err := loadMigrations(file("0001_init.sql", "select 1"))
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	two, err := loadMigrations(file("0001_init.sql", "select 2"))
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}

	if one[0].checksum == two[0].checksum {
		t.Error("different migration bodies produced the same checksum")
	}
	if one[0].checksum == "" {
		t.Error("checksum is empty")
	}
}

func TestLoadMigrationsRejectsABrokenSet(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"no migrations at all": {migrationDir: &fstest.MapFile{Mode: fs.ModeDir}},
		"a gap in the numbering": merge(
			file("0001_init.sql", "select 1"),
			file("0003_third.sql", "select 3"),
		),
		"not starting at one":    file("0002_second.sql", "select 2"),
		"a duplicate version":    merge(file("0001_a.sql", "select 1"), file("1_b.sql", "select 1")),
		"a name with no version": file("init.sql", "select 1"),
		"a version of zero":      file("0000_init.sql", "select 1"),
		"not sql":                file("0001_init.txt", "select 1"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadMigrations(fsys); !errors.Is(err, ErrBadMigration) {
				t.Errorf("loadMigrations = %v, want ErrBadMigration", err)
			}
		})
	}
}

// The embedded set is the one that actually ships, so it gets the same check.
func TestEmbeddedMigrationsAreAUsableSet(t *testing.T) {
	got, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no migrations are embedded")
	}
	if got[0].name != "init" {
		t.Errorf("first migration is %q, want init", got[0].name)
	}
}
