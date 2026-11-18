package github

import "testing"

// THE ALERT LISTING'S NEXT PAGE IS THE LINK HEADER'S CURSOR, and a next link with no cursor to
// read is an error rather than the last page: taken as the end, it would cut the open alerts
// short under a cover that says they are complete, and tombstone the rest.
func TestNextCursorReadsTheNextLinksAfterOrRefusesIt(t *testing.T) {
	for _, c := range []struct {
		link, want string
		fails      bool
	}{
		{link: "", want: ""},
		{link: `<https://api.github.com/repositories/1/dependabot/alerts?per_page=100&before=Y3Vy>; rel="prev"`, want: ""},
		{link: `<https://api.github.com/repositories/1/dependabot/alerts?before=Y3Vy>; rel="prev", ` +
			`<https://api.github.com/repositories/1/dependabot/alerts?per_page=100&after=Y3Vyc29y>; rel="next"`,
			want: "Y3Vyc29y"},
		{link: `<https://api.github.com/repositories/1/dependabot/alerts?page=2>; rel="next"`, fails: true},
		{link: `<://not a url>; rel="next"`, fails: true},
	} {
		got, err := nextCursor(c.link)
		if c.fails {
			if err == nil {
				t.Errorf("nextCursor(%q) = %q, want an error", c.link, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("nextCursor(%q) = %q, %v; want %q", c.link, got, err, c.want)
		}
	}
}
