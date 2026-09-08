package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/r4ph92/DevDuel/internal/store/storetest"
)

// db migrate against a real database, because the interesting part of the
// command is the half that only exists when there is a server on the other
// end: that it reports what it applied, and that a second run says so.
func TestMigrateBringsAnEmptyDatabaseUp(t *testing.T) {
	db := storetest.Empty(t)
	url := db.Config().ConnString()

	var out, errOut bytes.Buffer
	if err := run(t.Context(), []string{"db", "migrate", "-url", url}, &out, &errOut); err != nil {
		t.Fatalf("db migrate: %v\n%s", err, errOut.String())
	}
	if got := out.String(); !strings.Contains(got, "applied 0001_init") {
		t.Errorf("stdout = %q, want it to name the migration it applied", got)
	}

	out.Reset()
	if err := run(t.Context(), []string{"db", "migrate", "-url", url}, &out, &errOut); err != nil {
		t.Fatalf("second db migrate: %v\n%s", err, errOut.String())
	}
	if got := out.String(); !strings.Contains(got, "up to date") {
		t.Errorf("stdout = %q, want it to report an up to date database", got)
	}
}
