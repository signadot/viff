package main

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/signadot/verse/pkg/entity"
)

// Text and addresses as the screen handles them: what is cut to a column, what is made safe to
// lay out, and how one address stands to another. None of it draws; tuiview.go does.

// cut is s cut to w columns, its spacing kept (short collapses it, which a column cannot have).
func cut(s string, w int) string {
	// By the cells it takes on screen: a coloured string's escapes are no width, and a cut
	// counting them as runes left a question's row in the explorer at "o…".
	if ansi.StringWidth(s) <= w {
		return s
	}
	if w <= 1 {
		return ansi.Truncate(s, max(w, 0), "")
	}
	return ansi.Truncate(s, w, "…")
}

// pad is s cut or filled to w columns.
func pad(s string, w int) string {
	if lw := lipgloss.Width(s); lw > w {
		return lipgloss.NewStyle().MaxWidth(w).Render(s)
	} else if lw < w {
		return s + strings.Repeat(" ", w-lw)
	}
	return s
}

// lastShown is a line with carriage returns as a terminal leaves it: the last part with
// anything to see, the colours of the other parts kept around it (a part holding only a
// colour's end is not what shows).
func lastShown(l string) string {
	parts := strings.Split(l, "\r")
	at := len(parts) - 1
	for at > 0 && strings.TrimSpace(ansi.Strip(parts[at])) == "" {
		at--
	}
	sgr := func(ps []string) string {
		var b strings.Builder
		for _, p := range ps {
			for _, e := range escapeSeq.FindAllString(p, -1) {
				if strings.HasPrefix(e, "\x1b[") && strings.HasSuffix(e, "m") {
					b.WriteString(e)
				}
			}
		}
		return b.String()
	}
	return sgr(parts[:at]) + parts[at] + sgr(parts[at+1:])
}

// escapeSeq is any escape sequence: a CSI (ESC [ … final), an OSC (ESC ] … BEL or ST), or a
// two-byte escape.
var escapeSeq = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-Z\\\\-_]")

// screenSafe is text from the verse made safe to lay out: what the screen draws is measured by
// its cells, and a character the measure takes as no width while the terminal moves the cursor
// for it shifts or overwrites the rest of its line. A record carries them as it was written: a
// PR body's CRLF, a script's tabs, a progress bar's carriage returns, a log's cursor movements.
// So CRLF ends a line, a line keeps what follows its last carriage return (what a terminal
// would show), a tab is spaces to the next stop of 4, and every other control character and
// escape sequence goes, but colour (SGR), which the screen draws its own text in.
func screenSafe(s string) string {
	if !strings.ContainsAny(s, "\r\t\x1b\x00\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1c\x1d\x1e\x1f\x7f") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = escapeSeq.ReplaceAllStringFunc(s, func(e string) string {
		if strings.HasPrefix(e, "\x1b[") && strings.HasSuffix(e, "m") {
			return e
		}
		return ""
	})
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.ContainsRune(l, '\r') {
			l = lastShown(l)
		}
		var b strings.Builder
		col := 0
		for k := 0; k < len(l); {
			if l[k] == 0x1b { // a colour, kept whole and no width
				e := escapeSeq.FindString(l[k:])
				b.WriteString(e)
				k += max(len(e), 1)
				continue
			}
			r, n := utf8.DecodeRuneInString(l[k:])
			k += n
			switch {
			case r == '\t':
				stop := 4 - col%4
				b.WriteString(strings.Repeat(" ", stop))
				col += stop
			case r < 0x20 || r == 0x7f:
			default:
				b.WriteRune(r)
				col += ansi.StringWidth(string(r))
			}
		}
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

// ADDRESSES ARE COMPARED AS SEGMENTS, never as text: `x.pr."123"` is the place a task names as
// `x.pr.123`, and `verse.schedule."a.b:c"` is one segment deep in `verse.schedule`, though a
// cut at its last dot would say otherwise.

// segsOf is an address's segments; ok is false for one that does not parse (an index, say),
// which the callers then treat as text.
func segsOf(at string) ([]string, bool) {
	segs, err := entity.PathSegs(at)
	return segs, err == nil
}

// parentOf is the address one level above at, "" for the root.
func parentOf(at string) string {
	if segs, ok := segsOf(at); ok && len(segs) > 0 {
		return entity.JoinSegs(segs[:len(segs)-1])
	}
	if i := strings.LastIndexAny(at, ".["); i >= 0 {
		return at[:i]
	}
	return ""
}

// under is whether the address subject is at or beneath at; everything is beneath the root.
func under(subject, at string) bool {
	if at == "" {
		return true
	}
	s, ok1 := segsOf(subject)
	a, ok2 := segsOf(at)
	if !ok1 || !ok2 {
		return subject == at || strings.HasPrefix(subject, at+".") || strings.HasPrefix(subject, at+"[")
	}
	if len(s) < len(a) {
		return false
	}
	for i := range a {
		if s[i] != a[i] {
			return false
		}
	}
	return true
}

// tokenOf is an address as a task's id carries its subject: entity.SegToken, without the hash a
// lossy flattening adds (so that a place's token is a prefix of its children's).
func tokenOf(at string) string {
	if segs, ok := segsOf(at); ok {
		at = entity.SegToken(segs)
	}
	tok, _, _ := strings.Cut(at, entity.HashSep)
	return tok
}

// tokenUnder is whether a task's subject token is at or beneath the address at.
func tokenUnder(tok, at string) bool {
	if at == "" {
		return true
	}
	tok, _, _ = strings.Cut(tok, entity.HashSep)
	a := tokenOf(at)
	return tok == a || strings.HasPrefix(tok, a+".")
}
