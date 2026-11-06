package github

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A REVIEW COMMENT'S PAYLOAD SAYS WHERE IT IS AND WHAT IT ANSWERS, read from GitHub's review
// comment as it is sent: the file, the lines and side, the commit, the thread's first comment, and
// the hunk cut from the front. A conversation comment, read from GitHub's issue comment, gains
// none of them.
func TestACommentPayloadCarriesAReviewCommentsLocation(t *testing.T) {
	var body []string
	for i := 1; i <= 30; i++ {
		body = append(body, fmt.Sprintf(" line %d", i))
	}
	hunk := "@@ -10,30 +10,30 @@ func f() {\r\n" + strings.Join(body, "\r\n")
	raw, _ := json.Marshal(map[string]any{
		"id": 902, "body": "and here", "pull_request_url": "https://api.github.com/repos/acme/app/pulls/71",
		"path": "pkg/a.go", "line": 39, "start_line": 38, "side": "RIGHT", "commit_id": "cafe1234",
		"in_reply_to_id": 900, "diff_hunk": hunk, "original_line": 39,
	})
	var c comment
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	p := commentPayload("acme/app", c, "review")
	if p["path"] != "pkg/a.go" || p["line"] != 39 || p["startLine"] != 38 || p["side"] != "RIGHT" ||
		p["startSide"] != "RIGHT" || p["commitId"] != "cafe1234" || p["inReplyTo"] != int64(900) ||
		p["diffHunkTruncated"] != true {
		t.Errorf("payload: %v", p)
	}
	h := strings.Split(p["diffHunk"].(string), "\n")
	if len(h) != 21 || h[0] != "@@ -10,30 +10,30 @@ func f() {" || h[1] != " line 11" || h[20] != " line 30" {
		t.Errorf("the hunk, header and the last 20 lines with LF endings: %q", h)
	}

	var conv comment
	if err := json.Unmarshal([]byte(`{"id": 800, "body": "lgtm", "issue_url": "https://api.github.com/repos/acme/app/issues/71"}`), &conv); err != nil {
		t.Fatal(err)
	}
	// Even a conversation comment carrying review fields, were GitHub to send them, gains none.
	conv.Path, conv.DiffHunk, conv.InReplyToID = "x", "@@ -1 +1 @@", 1
	for k := range commentPayload("acme/app", conv, "conversation") {
		switch k {
		case "path", "line", "startLine", "startSide", "side", "commitId", "inReplyTo", "diffHunk", "diffHunkTruncated":
			t.Errorf("a conversation comment carries %s", k)
		}
	}
}

// A hunk is cut from the front, keeping its header: the last 20 lines, or a multi-line comment's
// lines and three before them where that is more. One short enough is left whole, a start the hunk
// does not hold keeps the last 20, and one without a header is cut by counting back.
func TestAReviewHunkIsCutFromTheFront(t *testing.T) {
	var body []string
	for i := 1; i <= 50; i++ {
		body = append(body, fmt.Sprintf("+%d", i))
	}
	hunk := "@@ -0,0 +1,50 @@\n" + strings.Join(body, "\n")
	right := func(start, span int) hunkAt {
		return hunkAt{side: "RIGHT", startSide: "RIGHT", start: start, span: span, original: true}
	}
	for _, tc := range []struct {
		name  string
		hunk  string
		at    hunkAt
		lines int
		first string
		cut   bool
	}{
		{"one line", hunk, right(0, 0), 21, "+31", true},
		{"a short span", hunk, right(45, 6), 21, "+31", true},
		{"a long span", hunk, right(21, 30), 34, "+18", true},
		{"a span the whole hunk", hunk, right(1, 50), 51, "+1", false},
		{"a start the hunk does not hold", "@@ -0,0 +11,50 @@\n" + strings.Join(body, "\n"), right(5, 0), 21, "+31", true},
		// The current lines are not the header's numbers: they are counted back, by side.
		{"current lines only", "@@ -0,0 +11,50 @@\n" + strings.Join(body, "\n"), hunkAt{side: "RIGHT", startSide: "RIGHT", start: 5, span: 30}, 34, "+18", true},
		{"short enough", "@@ -1 +1 @@\n+a\n+b", right(0, 0), 3, "+a", false},
		{"no header", strings.Join(body, "\n"), right(0, 0), 20, "+31", true},
		{"no header, a long span", strings.Join(body, "\n"), right(21, 30), 33, "+18", true},
	} {
		got, cut := reviewHunk(tc.hunk, tc.at)
		h := strings.Split(got, "\n")
		first := h[0]
		if strings.HasPrefix(first, "@@") {
			first = h[1]
		}
		if len(h) != tc.lines || first != tc.first || cut != tc.cut || h[len(h)-1] != strings.Split(tc.hunk, "\n")[len(strings.Split(tc.hunk, "\n"))-1] {
			t.Errorf("%s: %d lines from %q, cut %v; want %d from %q, cut %v", tc.name, len(h), first, cut, tc.lines, tc.first, tc.cut)
		}
	}
}

// interleaved is a hunk of 40 pairs of a removed and an added line, then a context line: a span of
// 30 lines on either side reaches back 60 hunk lines, not 30.
func interleaved(header bool, extra ...string) string {
	var body []string
	for i := 1; i <= 40; i++ {
		body = append(body, fmt.Sprintf("-old %d", i), fmt.Sprintf("+new %d", i))
	}
	body = append(body, extra...)
	body = append(body, " end")
	if header {
		body = append([]string{"@@ -1,41 +1,41 @@"}, body...)
	}
	return strings.Join(body, "\n")
}

// A MULTI-LINE COMMENT KEEPS THE LINE IT STARTS ON, found by numbering the hunk's lines from its
// header: a RIGHT span over many `-` lines, or a LEFT one over many `+` lines, is far more hunk
// lines than line numbers, and the cut keeps every line of it and three before.
func TestAReviewHunkKeepsTheStartLine(t *testing.T) {
	for _, tc := range []struct {
		side, first string
		lines       int
	}{
		// RIGHT from new line 12, "+new 12" at index 23: the header, three before it, and it and
		// the 58 lines after it.
		{"RIGHT", "-old 11", 1 + 3 + 58},
		// LEFT from old line 12, "-old 12" at index 22, and the 59 after it.
		{"LEFT", "+new 10", 1 + 3 + 59},
	} {
		got, cut := reviewHunk(interleaved(true), hunkAt{side: tc.side, startSide: tc.side, start: 12, span: 30, original: true})
		h := strings.Split(got, "\n")
		if !cut || len(h) != tc.lines || h[0] != "@@ -1,41 +1,41 @@" || h[1] != tc.first {
			t.Errorf("%s: %d lines from %q, cut %v; want %d from %q", tc.side, len(h), h[1], cut, tc.lines, tc.first)
		}
	}
}

// A COMMENT STARTING ON ONE SIDE AND ENDING ON THE OTHER keeps its whole range: from old line 11,
// through the removed and added lines, to new line 40. A count of the end side's lines would stop
// at the first `+` line and drop every `-` line in the range.
func TestAReviewHunkKeepsACrossSideRange(t *testing.T) {
	var body []string
	for i := 1; i <= 10; i++ {
		body = append(body, fmt.Sprintf(" ctx %d", i))
	}
	for i := 11; i <= 30; i++ {
		body = append(body, fmt.Sprintf("-old %d", i))
	}
	for i := 11; i <= 30; i++ {
		body = append(body, fmt.Sprintf("+new %d", i))
	}
	for i := 31; i <= 40; i++ {
		body = append(body, fmt.Sprintf(" ctx %d", i))
	}
	hunk := "@@ -1,40 +1,40 @@\n" + strings.Join(body, "\n")
	c := comment{Side: "RIGHT", StartSide: "LEFT", OriginalStartLine: intp(11), OriginalLine: intp(40)}
	if p := commentPayload("acme/app", comment{Side: "RIGHT", StartSide: "LEFT", StartLine: intp(11), Line: intp(40)}, "review"); p["startSide"] != "LEFT" || p["side"] != "RIGHT" {
		t.Errorf("a comment from LEFT to RIGHT says so: %v", p)
	}
	got, cut := reviewHunk(hunk, c.hunkAt())
	h := strings.Split(got, "\n")
	// "-old 11" at index 10, and three before it: from " ctx 8".
	if !cut || len(h) != 1+53 || h[1] != " ctx 8" || h[4] != "-old 11" {
		t.Errorf("%d lines from %q, cut %v; want 54 from \" ctx 8\"", len(h), h[1], cut)
	}
}

// Without a header the span is counted back from the end in lines of the comment's side, and a
// `\ No newline` marker is a line of neither. The marker stands mid-hunk here, which no real diff
// does (it follows the last line of its side): it is there to be skipped, not as an example.
func TestAHeaderlessHunkCountsItsSide(t *testing.T) {
	marker := `\ No newline at end of file`
	for _, tc := range []struct {
		side, first string
		lines       int
	}{
		{"RIGHT", "-old 11", 3 + 59},
		{"LEFT", "+new 10", 3 + 60},
	} {
		got, cut := reviewHunk(interleaved(false, marker), hunkAt{side: tc.side, startSide: tc.side, start: 12, span: 30})
		h := strings.Split(got, "\n")
		if !cut || len(h) != tc.lines || h[0] != tc.first {
			t.Errorf("%s: %d lines from %q, cut %v; want %d from %q", tc.side, len(h), h[0], cut, tc.lines, tc.first)
		}
	}
}

func intp(v int) *int { return &v }

// A COMMENT'S PLACE IN ITS HUNK IS READ FROM THE LINES IT WAS WRITTEN ON. GitHub nulls an
// outdated comment's line and start_line, and keeps original_line and original_start_line, which
// are what its hunk is numbered by; those are taken ahead of the current ones even when both are
// set. A span is only counted on one side.
func TestAReviewCommentsPlaceIsItsOriginalLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    comment
		want hunkAt
	}{
		{"outdated", comment{OriginalStartLine: intp(10), OriginalLine: intp(39)}, hunkAt{"RIGHT", "RIGHT", 10, 30, true}},
		{"original first", comment{Side: "LEFT", StartLine: intp(50), Line: intp(52), OriginalStartLine: intp(10), OriginalLine: intp(39)}, hunkAt{"LEFT", "LEFT", 10, 30, true}},
		{"current alone", comment{StartLine: intp(50), Line: intp(52)}, hunkAt{"RIGHT", "RIGHT", 50, 3, false}},
		{"across sides", comment{Side: "RIGHT", StartSide: "LEFT", OriginalStartLine: intp(11), OriginalLine: intp(40)}, hunkAt{"RIGHT", "LEFT", 11, 0, true}},
		{"one line", comment{Line: intp(52), OriginalLine: intp(39)}, hunkAt{"RIGHT", "RIGHT", 0, 0, false}},
	} {
		if got := tc.c.hunkAt(); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
