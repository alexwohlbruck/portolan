package main

import (
	"bytes"
	"flag"
	"io"
	"strings"
	"testing"
)

// The help text is data sitting beside the flags it describes, which is
// only an improvement over a hand-written page if the two are checked
// against each other. A renamed flag that quietly drops out of its group
// is exactly the drift that makes documentation lie.

func TestEveryGroupedFlagExists(t *testing.T) {
	for _, c := range commands {
		fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		c.flags(fs)
		for _, g := range c.groups {
			for _, n := range g.names {
				if fs.Lookup(n) == nil {
					t.Errorf("%s: help group %q lists --%s, which is not a flag",
						c.name, g.head, n)
				}
			}
		}
	}
}

func TestEveryFlagIsDocumented(t *testing.T) {
	for _, c := range commands {
		fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		c.flags(fs)
		fs.VisitAll(func(f *flag.Flag) {
			if strings.TrimSpace(f.Usage) == "" {
				t.Errorf("%s: --%s has no help text", c.name, f.Name)
			}
		})
		// a command that groups any of its flags should group all of
		// them, or the ungrouped ones land in a vague "other" bucket
		if len(c.groups) == 0 {
			continue // NOT return: that would skip every later command
		}
		grouped := map[string]bool{}
		for _, g := range c.groups {
			for _, n := range g.names {
				grouped[n] = true
			}
		}
		fs.VisitAll(func(f *flag.Flag) {
			if !grouped[f.Name] {
				t.Errorf("%s: --%s is in no help group", c.name, f.Name)
			}
		})
	}
}

func TestEveryCommandHasHelp(t *testing.T) {
	for _, c := range commands {
		if c.summary == "" {
			t.Errorf("%s: no summary", c.name)
		}
		if len(c.usage) == 0 {
			t.Errorf("%s: no usage line", c.name)
		}
		if len(c.example) == 0 {
			t.Errorf("%s: no example", c.name)
		}
		var buf bytes.Buffer
		c.printHelp(&buf)
		out := buf.String()
		if !strings.Contains(out, "portolan "+c.name) {
			t.Errorf("%s: help does not name the command:\n%s", c.name, out)
		}
		// usage lines are written without the leading "portolan ", and
		// the printer adds it — a line that carries its own would render
		// as "portolan portolan chart"
		for _, u := range c.usage {
			if strings.HasPrefix(u, "portolan ") {
				t.Errorf("%s: usage line %q must not repeat the binary name", c.name, u)
			}
			if !strings.HasPrefix(u, c.name) {
				t.Errorf("%s: usage line %q should start with the command name", c.name, u)
			}
		}
	}
}

func TestTopLevelUsageListsEveryCommand(t *testing.T) {
	var buf bytes.Buffer
	printUsage(&buf)
	out := buf.String()
	for _, c := range commands {
		if !strings.Contains(out, c.name) {
			t.Errorf("top-level usage omits %q", c.name)
		}
	}
	for _, extra := range []string{"version", "help"} {
		if !strings.Contains(out, extra) {
			t.Errorf("top-level usage omits %q", extra)
		}
	}
}

func TestFindResolvesEveryCommand(t *testing.T) {
	for _, c := range commands {
		if find(c.name) != c {
			t.Errorf("find(%q) did not return the command", c.name)
		}
	}
	if find("nope") != nil {
		t.Error("find returned a command for an unknown name")
	}
}
