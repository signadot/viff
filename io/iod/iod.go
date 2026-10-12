// Package iod serves the verse-facing source contract over HTTP, so a Source can be a
// program somebody deploys rather than a library verse has to be built with.
//
// It is two routes and no state: GET /io says what this program is, GET /reflect says
// what it currently sees. There is no write face, and that is the design rather than an
// omission — a source reflects, and converging the store with what it answered stays on
// verse's side (see verseio.Reflector). A source handed a way to write would be a source that
// could get the tombstone rules wrong.
//
// It is the same trick runnerd plays for runners, for the same reason: verse's own
// in-process source is the same value behind the same handler, so there is no privileged
// local path for a deployed one to differ from.
//
// BOTH ROUTES ANSWER IN THE WIRE FORM, because this is a payload one program sends another and
// that is what the wire form is for — logd, docd and `o` already speak it. The pretty rendering
// is a presentation, and presentation carries hazards a machine has no use for: it is
// indentation-sensitive, and a comment body ending a line in whitespace once produced a
// reflection verse could not read back, from a source that believed it had answered. Nothing
// about a reflection wants to be laid out; `verse io check` is where a person reads one.
package iod

import (
	"net/http"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
)

// Daemon serves one Source.
type Daemon struct {
	src verseio.Source
}

// New returns a daemon serving src.
func New(src verseio.Source) *Daemon { return &Daemon{src: src} }

// Handler is the contract: the two routes, and nothing else. A mux rather than a bare
// handler, so an unknown path answers 404 — which is the answer a client reads as "whatever
// is at this url, it does not serve this contract".
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+verseio.DescribePath, d.describe)
	mux.HandleFunc("GET "+verseio.ReflectPath, d.reflect)
	return mux
}

// describe answers what this program is. It has no effect on the source, which is what makes
// it usable as the install probe.
func (d *Daemon) describe(w http.ResponseWriter, r *http.Request) {
	codec.WriteWire(w, r, http.StatusOK, verseio.Describe(d.src))
}

// reflect answers what the source sees for the slice ?ref names — absent is the zero ref,
// which asks for everything it owns.
//
// A reflect that FAILED must fail loudly. An empty Reflection is a legitimate answer that
// carries an instruction (tombstone everything under these covers), so answering 200 with
// nothing because the read went wrong would spell a broken source as an emptied external
// system, and verse would converge to it.
func (d *Daemon) reflect(w http.ResponseWriter, r *http.Request) {
	var ref entity.Ref
	if s := r.URL.Query().Get("at"); s != "" {
		parsed, err := entity.ParseAddr(s)
		if err != nil {
			codec.HTTPError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		ref = parsed
	}
	rn, err := d.src.Reflect(r.Context(), ref)
	if err != nil {
		// 500 rather than 400: the request was well-formed and the external system did not
		// answer for it, which is a fact that fixes itself — and which verse reads as
		// unreachable rather than as "this is not a source".
		codec.HTTPError(w, r, http.StatusInternalServerError, "reflect: "+err.Error())
		return
	}
	codec.WriteWire(w, r, http.StatusOK, rn)
}
