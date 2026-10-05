package io

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// The obsolete input of the git io's out half: the questions a charter asked about things that are
// no longer proposed, settled `obsolete` (git-issue dzxn44gw: a question about a charters tree
// nothing proposes any more is not left pending on the docket).
//
//	to:     obsolete
//	prefix: "repo-charters.verse:propose@repo-charters.verse.candidate."   # the questions it may settle
//	keep:   {state: [<tree>, …], pulls: [<tree>, …]}                 # what is proposed now, at any depth
//	before: 2026-10-04T21:00:00Z                                     # only questions posed by then
//
// Every pending question that waits on this io, whose id begins with `prefix`, whose key (the id
// after the prefix, up to its `~<call>`) is none of the strings under `keep`, and that was posed at
// or before `before`, is answered `obsolete` as this io. `before` is the instant `keep` was read
// at: a question posed after it may be about something `keep` does not know of yet, and is left
// for the next sweep. A question that offers no `obsolete` is left alone. The status answers
// `obsoleted` (the ids answered this time), `sweptAt` and `before`.
//
// THE CHARTER DECIDES WHAT IS PROPOSED; this only applies it. The io is among the question's
// answerers because the charter that asks names it there, so nobody grants anything for it.

// obsoleteTarget is an obsolete input's `to`.
const obsoleteTarget = "obsolete"

// obsolete carries one obsolete input out, and answers the status fields.
func (k *GitSink) obsolete(ctx context.Context, in Input) (entity.Obj, error) {
	if k.Verse == nil || k.As == "" {
		return nil, Permanent(fmt.Errorf("this io reads no verse, so it settles no question"))
	}
	prefix := strings.TrimSpace(entity.Str(in.Value, "prefix"))
	before := strings.TrimSpace(entity.Str(in.Value, "before"))
	if prefix == "" || before == "" {
		return nil, Permanent(fmt.Errorf("an obsolete input carries {to, prefix, keep, before}"))
	}
	cutoff, err := time.Parse(time.RFC3339, before)
	if err != nil {
		return nil, Permanent(fmt.Errorf("`before` %q is not an RFC 3339 time", before))
	}
	if entity.Str(in.Status, "before") == before && entity.Get(in.Status, "error") == nil {
		return nil, nil // swept for this instant already
	}
	keep := map[string]bool{}
	collectStrings(entity.Get(in.Value, "keep"), keep)
	resp, err := k.Verse.Get("/asks", url.Values{"waiting": {"me"}})
	if err != nil {
		return nil, fmt.Errorf("listing the questions waiting on %s: %w", k.As, err)
	}
	body, err := client.OKBody(resp)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("listing the questions waiting on %s: %w", k.As, err)
	}
	list, err := codec.ParseNode(body, client.ResponseFormat(resp))
	if err != nil {
		return nil, fmt.Errorf("reading the questions waiting on %s: %w", k.As, err)
	}
	var settled []string
	for _, q := range ir.Uncomment(list).Values {
		id := entity.Str(q, "id")
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		key, _, _ := strings.Cut(strings.TrimPrefix(id, prefix), "~")
		if keep[key] || !offers(q, obsoleteTarget) {
			continue
		}
		if posed, err := time.Parse(time.RFC3339Nano, entity.Str(q, "posedAt")); err != nil || posed.After(cutoff) {
			continue
		}
		ans, err := k.Verse.Post("/asks/"+url.PathEscape(id)+"/answer", nil, api.Resolve{ID: id, Choice: obsoleteTarget, By: k.As})
		if err != nil {
			return nil, fmt.Errorf("settling %s: %w", id, err)
		}
		ans.Body.Close()
		switch {
		case ans.StatusCode == http.StatusNoContent || ans.StatusCode == http.StatusAccepted:
			settled = append(settled, id)
		case ans.StatusCode >= 500 || ans.StatusCode == http.StatusServiceUnavailable:
			return nil, fmt.Errorf("settling %s: %s", id, ans.Status)
		}
	}
	sort.Strings(settled)
	out := entity.Obj{"before": before, "sweptAt": k.now().UTC().Format(time.RFC3339)}
	if len(settled) > 0 {
		out["obsoleted"] = settled
	} else {
		out["obsoleted"] = entity.Removed()
	}
	return out, nil
}

// collectStrings adds every string at any depth of n to into.
func collectStrings(n *ir.Node, into map[string]bool) {
	n = ir.Uncomment(n)
	if n == nil {
		return
	}
	if n.Type == ir.StringType {
		into[n.String] = true
		return
	}
	for _, v := range n.Values {
		collectStrings(v, into)
	}
}

// offers is whether a question record offers the option id.
func offers(q *ir.Node, id string) bool {
	opts := ir.Uncomment(entity.Get(q, "options"))
	if opts == nil {
		return false
	}
	for _, o := range opts.Values {
		if o = ir.Uncomment(o); o == nil {
			continue
		}
		if (o.Type == ir.StringType && o.String == id) || entity.Str(o, "id") == id {
			return true
		}
	}
	return false
}
