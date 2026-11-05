package github_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/signadot/verse/pkg/entity"
	verseio "github.com/signadot/verse/pkg/io"
	"github.com/signadot/verse/pkg/io/github"
	verseiod "github.com/signadot/verse/pkg/io/iod"
)

// THE WHOLE CONTRACT, END TO END, WITH A BODY DESIGNED TO BREAK IT — which is the test that
// would have caught what shipping did not.
//
// Everything else in this package compares Reflections in memory, so none of them serialise
// anything, and the failure that reached a real repository was entirely in the serialisation: a
// comment body whose line ended in whitespace rendered as a block scalar that put the next
// sibling key at column zero, verse refused the document as undecodable, and every pass failed
// while the source looked alive. Two versions of tony and one workaround later, the structural
// answer is that this payload goes over the WIRE form, which has no indentation to get wrong.
//
// So this drives the real thing: the daemon's handler over real HTTP, read by the same
// verseio.HTTP client verse dials with.
func TestAHostileBodySurvivesTheHTTPContract(t *testing.T) {
	f := newFake()
	r := f.repo("acme/app")
	r.pr(1, "a change", "open", "2026-01-01T00:00:00Z", "cafe1234")
	r.issue(2, "an issue", "open", "2026-01-01T00:00:00Z")
	// Every shape that has broken the rendering, in one body: CRLF, a markdown hard line break
	// (two trailing spaces), a whitespace-only line, and a trailing blank line.
	r.comment(900, 1, "trailing space  \r\nand a blank line\n   \n\n", false, "2026-01-02T00:00:00Z")
	r.comment(901, 2, "```\ncode fence\n```\n", true, "2026-01-03T00:00:00Z")
	src := newSource(t, f, github.Config{Repos: []string{"acme/app"}})

	srv := httptest.NewServer(verseiod.New(src).Handler())
	defer srv.Close()
	client := verseio.HTTP("github", srv.URL, nil)

	// The probe an install makes, and then the reflection itself.
	if _, err := client.Describe(context.Background()); err != nil {
		t.Fatalf("the descriptor did not survive the contract: %v", err)
	}
	rn, err := client.Reflect(context.Background(), entity.Ref{})
	if err != nil {
		t.Fatalf("a real body did not survive the contract: %v", err)
	}
	got := payloads(rn)
	if body := got[`github.acme.app.comment."1"."900"`]["body"]; body != "trailing space  \nand a blank line\n   \n\n" {
		t.Fatalf("the body came back as %q", body)
	}
	if got[`github.acme.app.comment."2"."901"`] == nil {
		t.Fatalf("the review comment is missing: %v", got)
	}
}
