package tester_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/tester"
)

// requirements is the checklist the tests reconcile against.
func requirements(keys ...string) []challenge.Requirement {
	reqs := make([]challenge.Requirement, len(keys))
	for i, k := range keys {
		reqs[i] = challenge.Requirement{Key: k, Title: k, Description: k, Weight: 1}
	}
	return reqs
}

// document renders a result document, marker and all, after whatever noise
// the caller wants in front of it.
func document(noise, results string) string {
	return noise + tester.Marker + "\n" +
		`{"schema":"` + tester.Schema + `","results":[` + results + `]}` + "\n"
}

func result(key, status string) string {
	return `{"key":"` + key + `","status":"` + status + `","duration_ms":0,"message":""}`
}

// statuses renders the collected results as "key=status" for compact
// comparison.
func statuses(results []tester.Result) string {
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.Key + "=" + string(r.Status)
	}
	return strings.Join(parts, " ")
}

func collectOK(t *testing.T, reqs []challenge.Requirement, run tester.Run) []tester.Result {
	t.Helper()

	results, err := tester.Collect(reqs, run)
	if err != nil {
		t.Fatalf("Collect: unexpected error: %v", err)
	}
	if len(results) != len(reqs) {
		t.Fatalf("Collect returned %d results for %d requirements: %v", len(results), len(reqs), statuses(results))
	}
	return results
}

func TestCollectReportsEveryRequirementInSpecOrder(t *testing.T) {
	reqs := requirements("list-todos", "create-todo", "delete-todo")
	run := tester.Run{Stdout: document("", strings.Join([]string{
		result("delete-todo", "error"),
		result("create-todo", "pass"),
		result("list-todos", "fail"),
	}, ","))}

	results := collectOK(t, reqs, run)

	want := "list-todos=fail create-todo=pass delete-todo=error"
	if got := statuses(results); got != want {
		t.Errorf("Collect returned\n  %s\nwant\n  %s", got, want)
	}
}

func TestCollectReadsDurationsAndMessages(t *testing.T) {
	reqs := requirements("create-todo")
	run := tester.Run{Stdout: document("",
		`{"key":"create-todo","status":"fail","duration_ms":1250,"message":"expected 201, got 500"}`)}

	got := collectOK(t, reqs, run)[0]

	if got.Status != tester.StatusFail {
		t.Errorf("Status = %q, want %q", got.Status, tester.StatusFail)
	}
	if got.Duration != 1250*time.Millisecond {
		t.Errorf("Duration = %v, want 1.25s", got.Duration)
	}
	if got.Message != "expected 201, got 500" {
		t.Errorf("Message = %q", got.Message)
	}
}

func TestCollectRecordsAnUnreportedRequirementAsError(t *testing.T) {
	// The requirement the tester forgot must not read as a pass, and must not
	// vanish from the checklist either.
	reqs := requirements("list-todos", "create-todo")
	run := tester.Run{Stdout: document("", result("list-todos", "pass"))}

	results := collectOK(t, reqs, run)

	if got, want := statuses(results), "list-todos=pass create-todo=error"; got != want {
		t.Errorf("Collect returned\n  %s\nwant\n  %s", got, want)
	}
	if msg := results[1].Message; !strings.Contains(msg, "no result") {
		t.Errorf("the unreported requirement should say so, got %q", msg)
	}
}

func TestCollectSurvivesATesterThatProducedNothing(t *testing.T) {
	cases := []struct {
		name string
		run  tester.Run
	}{
		{"silent", tester.Run{Stdout: "", ExitCode: 0}},
		{"crashed", tester.Run{Stdout: "Traceback (most recent call last):\n", ExitCode: 1}},
		{"noise but no marker", tester.Run{Stdout: strings.Repeat("collecting ...\n", 100), ExitCode: 2}},
		{"timed out", tester.Run{Stdout: "", ExitCode: -1, Err: context.DeadlineExceeded}},
		{"never started", tester.Run{Err: errors.New("container exited before the tests ran")}},
	}

	reqs := requirements("list-todos", "create-todo")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			results := collectOK(t, reqs, c.run)

			if got, want := statuses(results), "list-todos=error create-todo=error"; got != want {
				t.Errorf("Collect returned\n  %s\nwant\n  %s", got, want)
			}
			for _, r := range results {
				if r.Message == "" {
					t.Errorf("%s: an error with no explanation is not useful", r.Key)
				}
			}
		})
	}
}

func TestCollectSaysWhyWhenTheTesterDidNotFinish(t *testing.T) {
	reqs := requirements("list-todos")
	run := tester.Run{Err: context.DeadlineExceeded}

	msg := collectOK(t, reqs, run)[0].Message

	if !strings.Contains(msg, context.DeadlineExceeded.Error()) {
		t.Errorf("message should carry why the run ended, got %q", msg)
	}
}

func TestCollectPrefersTheLastMarker(t *testing.T) {
	// A test that logs a response body can echo whatever the player's app
	// returned, marker and all. The tester writes its own document last, so
	// the last marker is the real one.
	spoofed := document("", result("list-todos", "pass")) + "the tests then carried on\n"
	genuine := document(spoofed, result("list-todos", "fail"))

	results := collectOK(t, requirements("list-todos"), tester.Run{Stdout: genuine})

	if results[0].Status != tester.StatusFail {
		t.Errorf("Status = %q, want the last document to win", results[0].Status)
	}
}

func TestCollectIgnoresAMarkerInsideALine(t *testing.T) {
	// Only a marker standing alone on its line introduces a document.
	noise := "log: the response contained " + tester.Marker + " somehow\n"
	run := tester.Run{Stdout: document(noise, result("list-todos", "fail"))}

	results := collectOK(t, requirements("list-todos"), run)

	if results[0].Status != tester.StatusFail {
		t.Errorf("Status = %q, want fail", results[0].Status)
	}
}

func TestCollectToleratesOutputAfterTheDocument(t *testing.T) {
	// Test runners print their own summary after the plugin has written the
	// document.
	run := tester.Run{Stdout: document("", result("list-todos", "pass")) + "\n=== 1 passed in 0.4s ===\n"}

	results := collectOK(t, requirements("list-todos"), run)

	if results[0].Status != tester.StatusPass {
		t.Errorf("Status = %q, want pass", results[0].Status)
	}
}

// assertProtocolError checks that the run fails the job and says why.
func assertProtocolError(t *testing.T, stdout string, wantReasons ...string) {
	t.Helper()

	_, err := tester.Collect(requirements("list-todos", "create-todo"), tester.Run{Stdout: stdout})
	if err == nil {
		t.Fatal("Collect: expected a protocol error, got nil")
	}

	var protoErr *tester.ProtocolError
	if !errors.As(err, &protoErr) {
		t.Fatalf("error is %T (%v), want *tester.ProtocolError", err, err)
	}
	for _, want := range wantReasons {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got:\n%v", want, err)
		}
	}
}

func TestCollectRejectsAnUnreadableDocument(t *testing.T) {
	cases := []struct {
		name    string
		stdout  string
		reasons []string
	}{
		{
			"not json at all",
			tester.Marker + "\nthe tester crashed here\n",
			[]string{"not valid JSON"},
		},
		{
			"truncated json",
			tester.Marker + "\n" + `{"schema":"` + tester.Schema + `","results":[`,
			[]string{"not valid JSON"},
		},
		{
			"nothing after the marker",
			"running tests\n" + tester.Marker + "\n",
			[]string{"nothing after"},
		},
		{
			"a different schema",
			strings.Replace(document("", result("list-todos", "pass")), tester.Schema, "devduel.results/2", 1),
			[]string{"devduel.results/2", tester.Schema},
		},
		{
			"a status nobody defined",
			document("", result("list-todos", "skipped")),
			[]string{"results[0].status", "skipped", "pass"},
		},
		{
			"a key the challenge never declared",
			document("", result("list-todoz", "pass")),
			[]string{"results[0].key", "list-todoz", "not a declared requirement"},
		},
		{
			"the same key twice",
			document("", result("list-todos", "pass")+","+result("list-todos", "fail")),
			[]string{"results[1].key", "twice"},
		},
		{
			"a key that is missing",
			document("", `{"status":"pass","duration_ms":0,"message":""}`),
			[]string{"results[0].key", "required"},
		},
		{
			"a negative duration",
			document("", `{"key":"list-todos","status":"pass","duration_ms":-5,"message":""}`),
			[]string{"results[0].duration_ms", "negative"},
		},
		{
			"a misspelled field",
			document("", `{"key":"list-todos","status":"pass","duration":12,"message":""}`),
			[]string{"duration"},
		},
		{
			"a field of the wrong type",
			document("", `{"key":"list-todos","status":"pass","duration_ms":"fast","message":""}`),
			[]string{"duration_ms", "wrong type"},
		},
		{
			"no schema at all",
			tester.Marker + "\n" + `{"results":[` + result("list-todos", "pass") + `]}` + "\n",
			[]string{"no schema", tester.Schema},
		},
		{
			"an empty status",
			document("", `{"key":"list-todos","status":"","duration_ms":0,"message":""}`),
			[]string{"results[0].status", "required"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertProtocolError(t, c.stdout, c.reasons...)
		})
	}
}

func TestCollectReportsEveryProblemAtOnce(t *testing.T) {
	stdout := document("", strings.Join([]string{
		result("list-todos", "skipped"),
		result("nope", "pass"),
	}, ","))

	assertProtocolError(t, stdout, "2 problems", "results[0].status", "results[1].key")
}

func TestCollectShowsWhatItActuallyRead(t *testing.T) {
	_, err := tester.Collect(requirements("list-todos"), tester.Run{
		Stdout: tester.Marker + "\n<html>the app returned a page</html>\n",
	})
	if err == nil {
		t.Fatal("Collect: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "<html>") {
		t.Errorf("error should quote what it tried to read, got:\n%v", err)
	}
}

func TestCollectTrimsALongExcerpt(t *testing.T) {
	junk := strings.Repeat("the app returned a page instead of results ", 40)

	_, err := tester.Collect(requirements("list-todos"), tester.Run{Stdout: tester.Marker + "\n" + junk})
	if err == nil {
		t.Fatal("Collect: expected an error, got nil")
	}

	msg := err.Error()
	if len(msg) > 600 {
		t.Errorf("error is %d characters; a flood of junk should be excerpted, not reprinted", len(msg))
	}
	if !strings.Contains(msg, "...") {
		t.Error("a trimmed excerpt should show that it was trimmed")
	}
}

func TestCollectHandlesAChallengeWithNoRequirements(t *testing.T) {
	results, err := tester.Collect(nil, tester.Run{Stdout: document("", "")})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Collect returned %v, want nothing", statuses(results))
	}
}

func TestParseReportsMissingResultsSeparately(t *testing.T) {
	// Collect absorbs a missing document; Parse hands it back so a caller
	// like `devduelctl challenge verify` can tell the two apart.
	_, err := tester.Parse("no marker in here\n")

	if !errors.Is(err, tester.ErrNoResults) {
		t.Fatalf("Parse: got %v, want ErrNoResults", err)
	}
}

func TestParseReturnsWhatTheTesterReported(t *testing.T) {
	results, err := tester.Parse(document("noise\n", result("create-todo", "pass")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(results) != 1 || results[0].Key != "create-todo" {
		t.Fatalf("Parse returned %v, want the one reported result", statuses(results))
	}
}
