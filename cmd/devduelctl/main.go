// Command devduelctl is DevDuel's operator tool.
//
// It verifies challenges: deciding whether one is fit to be played, by
// building it and judging it against its own workspace and solution.
// Everything about a challenge that cannot be checked by reading it is
// checked here, before anyone is ranked on it.
//
// It also migrates the database, which is deliberately a command an operator
// runs rather than something a server does to itself on boot. Several API
// instances starting at once would all try, and a schema change is a thing
// somebody should be watching.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/r4ph92/DevDuel/internal/challenge"
	"github.com/r4ph92/DevDuel/internal/container"
	"github.com/r4ph92/DevDuel/internal/judge"
	"github.com/r4ph92/DevDuel/internal/store"
	"github.com/r4ph92/DevDuel/internal/verify"
)

func main() {
	// Ctrl-C has to reach the judge rather than the process, or an
	// interrupted verification leaves containers behind.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, errReported) {
			fmt.Fprintln(os.Stderr, "devduelctl:", err)
		}
		os.Exit(1)
	}
}

// errReported means the failure has already been written out in full, and
// main should exit non-zero without adding to it.
var errReported = errors.New("reported")

const usage = `devduelctl is the DevDuel operator tool.

Usage:
  devduelctl challenge verify <dir>   build a challenge and check it behaves as declared
  devduelctl db migrate               bring the database up to the schema this binary carries
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	help := &printer{w: stderr}

	if len(args) < 2 {
		help.print(usage)
		return errors.New("unknown command")
	}

	switch args[0] + " " + args[1] {
	case "challenge verify":
		return runVerify(ctx, args[2:], stdout, stderr)
	case "db migrate":
		return runMigrate(ctx, args[2:], stdout, stderr)
	default:
		help.print(usage)
		return errors.New("unknown command")
	}
}

func runVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("challenge verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		(&printer{w: stderr}).print(usage)
		return errors.New("challenge verify takes exactly one directory")
	}

	runtime := container.NewCLI()
	return verifyChallenge(ctx, fs.Arg(0), runtime, judge.New(runtime), stdout)
}

// envDatabaseURL is where the connection string comes from when -url is not
// given, so that a password never has to appear in a shell history.
const envDatabaseURL = "DATABASE_URL"

func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("db migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", os.Getenv(envDatabaseURL), "postgres connection string, defaults to $"+envDatabaseURL)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		(&printer{w: stderr}).print(usage)
		return errors.New("db migrate takes no arguments")
	}
	if *url == "" {
		return fmt.Errorf("no database: pass -url or set $%s", envDatabaseURL)
	}

	db, err := store.Open(ctx, *url)
	if err != nil {
		return err
	}
	defer db.Close()

	applied, err := store.Migrate(ctx, db)
	if err != nil {
		return err
	}

	p := &printer{w: stdout}
	if len(applied) == 0 {
		p.print("database is up to date\n")
		return p.err
	}
	for _, m := range applied {
		p.printf("applied %04d_%s\n", m.Version, m.Name)
	}
	p.printf("%s applied\n", plural(len(applied), "migration"))
	return p.err
}

// verifyChallenge loads, verifies and reports on one challenge directory.
func verifyChallenge(ctx context.Context, dir string, b verify.Builder, r verify.Runner, out io.Writer) error {
	spec, err := challenge.Load(dir)
	if err != nil {
		return err
	}

	// Printed before the build rather than after it, because a build is the
	// slowest thing here and silence looks like a hang.
	p := &printer{w: out}
	p.printf("verifying %s\n", spec.Key())
	p.printf("  building %s\n", spec.Image.Tag)

	report, err := verify.New(b, r).Challenge(ctx, spec)
	if err != nil {
		return err
	}

	writeReport(p, report)
	if p.err != nil {
		return p.err
	}
	if !report.Valid() {
		return errReported
	}
	return nil
}

// printer writes progress and remembers the first failure, so the reporting
// code reads as reporting rather than as error handling.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) print(s string) { p.printf("%s", s) }

func (p *printer) printf(format string, args ...any) {
	if p.err != nil {
		return
	}
	_, p.err = fmt.Fprintf(p.w, format, args...)
}

func writeReport(p *printer, report verify.Report) {
	for _, phase := range []verify.Phase{verify.PhaseWorkspace, verify.PhaseSolution, verify.PhaseRepeat} {
		results := report.Runs[phase]
		passed := 0
		for _, r := range results {
			if r.Status == "pass" {
				passed++
			}
		}
		p.printf("  %-9s %d of %d requirements pass\n", phase, passed, len(results))
	}

	if report.Valid() {
		p.printf("\n%s is valid\n", report.Challenge)
		return
	}

	p.printf("\n%s is not valid, %s:\n", report.Challenge, plural(len(report.Problems), "problem"))
	for _, prob := range report.Problems {
		p.printf("  %s\n", prob)
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
