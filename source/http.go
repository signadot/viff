package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// The two routes an external source serves. They are the whole contract: one question about
// what it is, one about what it sees.
const (
	// DescribePath answers what this program is: GET /source → Descriptor.
	DescribePath = "/source"
	// ReflectPath answers what it sees: GET /reflect[?ref=<ref>] → Reflection. A missing or
	// empty ref is the zero ref, which asks for everything the source owns.
	ReflectPath = "/reflect"
)

// Descriptor is what a source says it is when asked, and it is what an install probes for.
//
// Owns is the ownership claim the registration is checked against — coarser than a
// Reflection's Covers, which is per-pass. It is optional: a source that declines to say
// cannot have its disjointness from the other installed sources PROVED, which is a fact
// about the install rather than an error here.
type Descriptor struct {
	System string           `tony:"field=system"`
	Owns   []entity.Pattern `tony:"field=owns,omitzero"`
}

// Owner is the optional face a source uses to declare what it claims to be the single writer
// of. It is separate from Source because a source deployed alone, into a verse with no other
// source of its system, needs nothing from it.
type Owner interface {
	Owns() []entity.Pattern
}

// Describe builds the descriptor for a source, asking it what it owns if it will say.
func Describe(src Source) Descriptor {
	d := Descriptor{System: src.System()}
	if o, ok := src.(Owner); ok {
		d.Owns = o.Owns()
	}
	return d
}

// The two facts a caller has to be able to tell apart, and they are the runner's two for the
// runner's reasons: whether waiting could change the answer.
var (
	// ErrUnreachable: nothing answered, or what answered was failing rather than refusing.
	// It can fix itself.
	ErrUnreachable = errors.New("source unreachable")
	// ErrNotSource: something answered and is not a source. It cannot.
	ErrNotSource = errors.New("not a source")
)

// ReflectTimeout bounds a call to a remote source. A remote that accepts the connection and
// then never answers is indistinguishable from a slow one, and without a deadline the
// reflector's loop waits on that distinction forever.
//
// Generous rather than tight, and more so than a runner's control timeout: a reflect reads an
// external system — every ref in a large repo, a page of issues over somebody else's API —
// and it is a backstop against a hung peer, not a latency budget. A caller wanting a tighter
// bound passes a context.
const ReflectTimeout = 2 * time.Minute

// Client is a Source reached over HTTP: the same contract as an in-process one, with the
// answers arriving from another program. See docs/working/sources.md.
type Client struct {
	system string
	base   string
	cl     *http.Client
}

var _ Source = (*Client)(nil)

// HTTP returns the source at baseURL, which owns `system`.
//
// The system is passed rather than discovered, and that is not a shortcut. `Source.System()`
// cannot fail — it is asked on every converge — while discovering it costs a call that can,
// so a client that learned it lazily would have to answer the question wrongly or block on a
// network at an unrelated moment. The registration names the system (`sources:<system>:<name>`
// is the address), so verse always has it; whether the far side AGREES is a separate question,
// asked once at install by Probe, where an operator is present to hear the answer.
//
// tr is the transport, and it is the only thing that changes for a source verse cannot dial:
// a dial-in source's transport opens a connection the source itself made, and everything below
// stays as it is. nil takes http.DefaultTransport.
func HTTP(system, baseURL string, tr http.RoundTripper) *Client {
	return &Client{
		system: system,
		base:   strings.TrimRight(baseURL, "/"),
		// Its OWN client, never http.DefaultClient: that one is shared process-wide, so the
		// timeout below would silently bound every other caller's requests too.
		cl: &http.Client{Timeout: ReflectTimeout, Transport: tr},
	}
}

// System is the slice this source owns, as the registration named it.
func (c *Client) System() string { return c.system }

// Describe asks the source what it is. It is the cheapest question that has an answer, and it
// has no effect on the source — which is what makes it the probe.
func (c *Client) Describe(ctx context.Context) (Descriptor, error) {
	var d Descriptor
	if err := c.get(ctx, DescribePath, &d); err != nil {
		return Descriptor{}, err
	}
	if d.System == "" {
		return Descriptor{}, fmt.Errorf("%w: %s described itself with no system", ErrNotSource, c.base)
	}
	return d, nil
}

// Reflect asks the source what it sees for the slice ref names.
//
// EVERY FAILURE IS AN ERROR, and none of them is an empty Reflection. That is the sharpest
// thing this file has to get right: an empty answer carrying covers is a legitimate
// instruction to tombstone everything under them, so a status this did not read, or a body it
// could not decode, quietly returned as the zero value would be a transport fault spelled as
// "the external system is empty now".
func (c *Client) Reflect(ctx context.Context, ref entity.Ref) (Reflection, error) {
	path := ReflectPath
	// The zero ref is spelled by ABSENCE rather than by an empty parameter, so a source
	// reading `?ref=` as a ref rather than as nothing cannot mistake it for one.
	if ref != (entity.Ref{}) {
		path += "?ref=" + url.QueryEscape(ref.String())
	}
	var rn Reflection
	if err := c.get(ctx, path, &rn); err != nil {
		return Reflection{}, err
	}
	return rn, nil
}

// get issues one GET and decodes it, keeping the three ways it can fail apart.
func (c *Client) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnreachable, c.base, err)
	}
	req.Header.Set("Accept", codec.MediaType(codec.Tony))
	resp, err := c.cl.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnreachable, c.base, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnreachable, c.base, err)
	}
	// A 404 or a 501 says the same thing: whatever is at this url, it does not serve this
	// contract. That is an answer, and waiting will not change it.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNotImplemented {
		return fmt.Errorf("%w: %s does not serve %s", ErrNotSource, c.base, path)
	}
	// Everything else that failed is UNREACHABLE rather than not-a-source, including a 500.
	// Something at that url is speaking HTTP and failing, which is a source having a bad day
	// at least as often as it is the wrong url — and refusing an install on it would be
	// refusing a fact that fixes itself.
	if resp.StatusCode >= http.StatusBadRequest {
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = resp.Status
		}
		return fmt.Errorf("%w: %s: %s", ErrUnreachable, c.base, detail)
	}
	f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type"))
	if !ok {
		f = codec.Tony
	}
	if err := codec.Unmarshal(body, v, f); err != nil {
		return fmt.Errorf("%w: %s answered %s undecodably: %w", ErrNotSource, c.base, path, err)
	}
	return nil
}

// Reach is what a url said when asked, and it is the whole of what verse knows about a
// source's health. There is no fourth state: a probe is answered live, reported, and
// discarded.
type Reach int

const (
	// IsSource: it answered, and the body decoded as the descriptor the contract promises.
	IsSource Reach = iota
	// NotSource: it answered, and is not that.
	NotSource
	// Unreachable: nothing answered, or what answered was failing. Both are facts that can
	// change on their own, which is what separates this from NotSource.
	Unreachable
)

func (r Reach) String() string {
	switch r {
	case IsSource:
		return "a source"
	case NotSource:
		return "not a source"
	default:
		return "unreachable"
	}
}

// Probe asks a url what it is, and returns what it said.
//
// The descriptor comes back with the reach because it is the point: an install checks that the
// system the far side claims is the one the operator named, and reads its ownership to prove
// disjointness from what is already installed. A reach alone would answer the smaller half of
// the question.
func Probe(ctx context.Context, baseURL string, tr http.RoundTripper) (Reach, Descriptor, error) {
	d, err := HTTP("", baseURL, tr).Describe(ctx)
	switch {
	case err == nil:
		return IsSource, d, nil
	case errors.Is(err, ErrNotSource):
		return NotSource, Descriptor{}, err
	default:
		return Unreachable, Descriptor{}, err
	}
}
