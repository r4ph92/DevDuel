// Command devduelctl is the challenge authoring tool.
//
// Its one job today is verification: deciding whether a challenge is fit to be
// played, by building it and judging it against its own workspace and
// solution. Everything about a challenge that cannot be checked by reading it
// is checked here, before anyone is ranked on it.
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

const usage = `devduelctl is the DevDuel challenge authoring tool.

Usage:
  devduelctl challenge verify <dir>   build a challenge and check it behaves as declared
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	help := &printer{w: stderr}

	if len(args) < 2 || args[0] != "challenge" || args[1] != "verify" {
		help.print(usage)
		return errors.New("unknown command")
	}

	fs := flag.NewFlagSet("challenge verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		help.print(usage)
		return errors.New("challenge verify takes exactly one directory")
	}

	runtime := container.NewCLI()
	return verifyChallenge(ctx, fs.Arg(0), runtime, judge.New(runtime), stdout)
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
