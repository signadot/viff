package main

import (
	"strings"
	"testing"
)

// The docket line for a who-motion has to show what each option would RUN AS, not only what
// it is called. Once a doer brings its own grant and tool set, a voter choosing between two
// names is choosing between two authorities — and a ballot that hid that would have the group
// authorise a capability nobody put in front of them.
func TestOptionLabelsShowTheEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []any
		want string
	}{{
		// The case that must not regress: doers differing only by model still read as bare
		// names, so the common ballot stays as terse as it was.
		name: "interchangeable doers stay bare",
		opts: []any{
			map[string]any{"id": "gpt", "choice": "gpt", "need": int64(2)},
			map[string]any{"id": "claude", "choice": "claude", "need": int64(2)},
		},
		want: "gpt(2) claude(2)",
	}, {
		name: "a doer that narrows authority says so",
		opts: []any{
			map[string]any{"id": "fixer", "choice": "fixer", "grant": "no-delegate",
				"tools": []any{"read_file", "run_bash"}},
			map[string]any{"id": "memo", "choice": "memo", "grant": "read-only"},
		},
		want: "fixer[no-delegate +read_file,run_bash] memo[read-only]",
	}, {
		name: "tools without a grant",
		opts: []any{map[string]any{"id": "narrow", "tools": []any{"read_file"}}},
		want: "narrow[+read_file]",
	}, {
		// A whether-gate carries no envelope, and its id=choice rendering is untouched.
		name: "a whether-gate is unchanged",
		opts: []any{
			map[string]any{"id": "proceed", "choice": "approve", "need": int64(2)},
			map[string]any{"id": "deny", "choice": "reject"},
		},
		want: "proceed=approve(2) deny=reject",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := optionLabels(tc.opts); got != tc.want {
				t.Errorf("optionLabels\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// No options, or a payload that is not a list, renders nothing rather than a stray bracket.
func TestOptionLabelsEmpty(t *testing.T) {
	for _, v := range []any{nil, []any{}, "not a list"} {
		if got := optionLabels(v); got != "" {
			t.Errorf("optionLabels(%v) = %q, want empty", v, got)
		}
	}
}

// `verse provider` needs no envelope-specific code — it prints every payload key beside the
// driver — but that is a property worth pinning rather than assuming, since it is the surface
// an operator uses to answer "what would this doer be allowed to do" before voting.
func TestProvidersListsTheEnvelope(t *testing.T) {
	serve(t)

	if _, err := runCLI(t, "entity", "put", "provider:treat:fixer",
		`{driver: llm, risk: low, grant: no-delegate, tools: [read_file, run_bash]}`); err != nil {
		t.Fatalf("register: %v", err)
	}
	out, err := runCLI(t, "providers")
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	for _, want := range []string{"treat", "fixer", "driver=llm", "grant=no-delegate", "tools=", "run_bash"} {
		if !strings.Contains(out, want) {
			t.Errorf("providers output %q is missing %q", out, want)
		}
	}
}

// A docket line is one line, so a subject's `says` is trimmed to fit — and a paragraph is what
// a review actually produces.
//
// The line used to be `whether open` plus a ref, which told a voter nothing about what they
// were approving: they had to fetch the subject, read a branch name and two shas, and go find
// the diff elsewhere. A gate answered without knowing what it is about is answered by reflex.
func TestASubjectsSaysIsTrimmedToALine(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"a short claim": {
			in:   "claimed the first TWELVE characters; it returns eight",
			want: "claimed the first TWELVE characters; it returns eight",
		},
		"only the first line of a paragraph": {
			in:   "fixed one comment on main.shortSha\n\nand here is a great deal more prose",
			want: "fixed one comment on main.shortSha",
		},
		"nothing at all":    {in: "", want: ""},
		"surrounding space": {in: "   trimmed   ", want: "trimmed"},
	} {
		if got := firstLineOf(tc.in, 100); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
	long := strings.Repeat("x", 140)
	got := firstLineOf(long, 100)
	if len([]rune(got)) > 101 || !strings.HasSuffix(got, "…") {
		t.Errorf("a long claim was not cut to fit a line: %d chars, %q", len([]rune(got)), got[:20])
	}
}
