// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

// Package cli holds the commands of the takecare command (spec 13): it
// works on files and reads the cluster, and never writes into a cluster.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

// Exit codes, the same for every command (spec 13 §3).
const (
	// ExitOK: done; for status, everything Ready.
	ExitOK = 0
	// ExitFinding: a finding, such as something not Ready.
	ExitFinding = 1
	// ExitError: the command could not run.
	ExitError = 2
)

const (
	outputText = "text"
	outputJSON = "json"
)

const usage = `takecare: set up and keep a TakeCare install. It never writes into a cluster.

Usage:
  takecare <command> [flags]

Commands:
  init      ask factual questions and write the first files of an install, on a local branch
  status    show what the install says about itself (reads the cluster)
  discover  show an Intendant's newest survey and how to start a new one
  review    turn a survey's proposals into Map files, on a local branch
  validate  check the Map files and question files, offline
  version   print the version, the commit and the Go version

Run "takecare <command> -h" for a command's flags.
`

// app is one run: its streams and its clock. terminal tells whether
// stdin is one: without it, no command prompts. cliVersion is this
// takecare's version, compared with the cluster's (skew.go).
type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	terminal       bool
	now            func() time.Time
	cliVersion     string
}

// Run runs the command line args (without the program name) and returns
// the exit code. It reads answers from the process's standard input, when
// that is a terminal.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	info, err := os.Stdin.Stat()
	terminal := err == nil && info.Mode()&os.ModeCharDevice != 0
	a := &app{stdin: os.Stdin, stdout: stdout, stderr: stderr, terminal: terminal, now: time.Now,
		cliVersion: buildInfo(debug.ReadBuildInfo).Version}
	return a.run(ctx, args)
}

func (a *app) run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(a.stderr, usage)
		return ExitError
	}
	switch args[0] {
	case "init":
		return a.init(ctx, args[1:])
	case "status":
		return a.status(ctx, args[1:])
	case "discover":
		return a.discover(ctx, args[1:])
	case "review":
		return a.review(ctx, args[1:])
	case "validate":
		return a.validate(args[1:])
	case "version":
		return a.version(args[1:])
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(a.stdout, usage)
		return ExitOK
	default:
		_, _ = fmt.Fprintf(a.stderr, "takecare: unknown command %q\n\n%s", args[0], usage)
		return ExitError
	}
}

// common are the flags every command takes. Only init's forms draw a full
// screen or print color: --no-color turns their colors off, --accessible
// turns them into line prompts. The other commands print no color, and
// review asks with line prompts; they accept both flags so that a script
// can pass them to every command.
type common struct {
	output     string
	noColor    bool
	accessible bool
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.output, "output", outputText, "output format: text or json")
	fs.BoolVar(&c.noColor, "no-color", false, "print no color")
	fs.BoolVar(&c.accessible, "accessible", false, "line prompts instead of full screens")
}

// newFlags returns a command's flag set, writing its usage to stderr.
func (a *app) newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("takecare "+name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	return fs
}

// parse parses args and checks the common flags. It returns the exit code
// to stop with, or -1 to go on.
func (a *app) parse(fs *flag.FlagSet, c *common, args []string) int {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		return ExitError
	}
	if fs.NArg() > 0 {
		return a.fail("%s: unexpected argument %q", command(fs), fs.Arg(0))
	}
	if c.output != outputText && c.output != outputJSON {
		return a.fail("%s: --output must be text or json, got %q", command(fs), c.output)
	}
	return -1
}

// command is the name of the command fs parses.
func command(fs *flag.FlagSet) string { return strings.TrimPrefix(fs.Name(), "takecare ") }

// fail prints the reason the command could not run and returns ExitError.
func (a *app) fail(format string, args ...any) int {
	_, _ = fmt.Fprintf(a.stderr, "takecare: "+format+"\n", args...)
	return ExitError
}

// printJSON prints v, indented, on stdout.
func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
