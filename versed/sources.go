package versed

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/source"
)

// ── sources: what reflects an external system into the verse ─────────────────
//
// A source is installed at `sources:<system>:<name>` carrying one way in, and it is the single
// writer of its system's slice. Where a runner's kind is the operator's own category, a
// source's system is already a question verse asks — who writes `git:*` — so it goes in the
// address, and an overlap is visible in a listing. See docs/working/sources.md.

// sourceFor builds the Source a registration names. The three ways in differ only in where the
// net.Conn comes from; Reflect is the same call down all three.
//
// It is here rather than in package source because `dir` needs a *source.Git built in this
// process, which is a choice about how verse hosts a source rather than part of the contract.
func (s *Server) sourceFor(e source.Entity) (source.Source, error) {
	switch {
	case e.URL != "":
		return source.HTTP(e.System, e.URL, nil), nil
	case e.Dir != "":
		if e.System != source.GitSystem {
			return nil, fmt.Errorf("source %q: `dir` is the in-process git source, so its "+
				"system must be %q, not %q", e.Name, source.GitSystem, e.System)
		}
		g := source.NewGit()
		g.AddRepo("", e.Dir) // the dir's base name is the repo id
		return g, nil
	default:
		// Check has already refused dial-in, so this is a registration that got in some other
		// way — a hand-written entity, or a field this build does not know.
		return nil, fmt.Errorf("source %q: no way in", e.Name)
	}
}

// describe asks a registration what it is, and is the whole of the install probe.
//
// A `dir` source is described WITHOUT a network, by building it and asking it — which is the
// same question `sourced` answers over HTTP, put to the same value. That is what keeps `dir`
// first-class rather than a path with fewer checks than the others.
func (s *Server) describe(r *http.Request, e source.Entity) (source.Reach, source.Descriptor, error) {
	if e.Dir != "" {
		src, err := s.sourceFor(e)
		if err != nil {
			return source.NotSource, source.Descriptor{}, err
		}
		return source.IsSource, source.Describe(src), nil
	}
	return source.Probe(r.Context(), e.URL, nil)
}

// addSource installs one, after asking it what it is.
//
// It is addRunner with two checks added, and both are what an external source loses that an
// in-process one had by construction: that it is the system somebody named, and that nobody
// else already writes the slice it claims.
func (s *Server) addSource(w http.ResponseWriter, r *http.Request) {
	// Read as a DOCUMENT first, so a field the decoder would drop can be named. It matters more
	// here than for a runner: a dropped `owns` installs a source that claims nothing, and a
	// dropped `scope` reflects into baseline where everybody sees it.
	node, err := codec.ReadNode(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := source.CheckFields(node); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var e source.Entity
	if err := codec.FromNode(node, &e); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := e.Check(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	store := s.scopedStore(r)
	out := map[string]any{"ref": source.Ref(e.System, e.Name).String()}

	if r.URL.Query().Get("probe") != "false" {
		reach, d, why := s.describe(r, e)
		out["reach"] = reach.String()
		switch reach {
		case source.NotSource:
			http.Error(w, fmt.Sprintf("source %q: %s is not a source: %s — --no-probe installs "+
				"it anyway", e.Name, e.URL, why), http.StatusBadRequest)
			return
		case source.Unreachable:
			// Installed, and said so. A manifest landing before its pod is the ordinary state
			// of a source deployed alongside the entity that names it, so refusing would make
			// deployment order-dependent for a fact that fixes itself. The warning rides on the
			// response, because the operator running the command is who can act on it.
			out["warning"] = why.Error()
		default:
			// The check with no runner analogue. A `github` registration pointed at a git
			// source is the mistake this catches at the moment somebody is present — and it is
			// unrecoverable later, because every reflection would be written into the wrong
			// slice by a source that thinks it is right.
			if d.System != e.System {
				http.Error(w, fmt.Sprintf("source %q: you named system %q, but it says it is "+
					"%q — one of the two is wrong, and installing would write %s:* from a "+
					"source that reflects %s:*", e.Name, e.System, d.System, e.System, d.System),
					http.StatusBadRequest)
				return
			}
			// An ownership claim the operator did not write is taken from what the source SAID,
			// which is how `verse source add git:laptop <url>` gets one at all. It is a snapshot
			// exactly as the url is: what the source claimed when somebody installed it.
			if len(e.Owns) == 0 && len(d.Owns) > 0 {
				e.Owns = d.Owns
				out["owns"] = d.Owns
			}
		}
	}

	// Single-writer is what makes a reflected slice need no CAS. In process it held because
	// there was one Git value; across deployments nothing holds it but this.
	if r.URL.Query().Get("allow-overlap") != "true" {
		if why := overlaps(store, e); why != "" {
			http.Error(w, why, http.StatusConflict)
			return
		}
	}

	body := entity.Obj{"name": e.Name, "system": e.System}
	for k, v := range map[string]string{
		"url": e.URL, "dir": e.Dir, "scope": e.Scope,
	} {
		if v != "" {
			body[k] = v
		}
	}
	if len(e.Owns) > 0 {
		body["owns"] = e.Owns
	}
	if _, _, err := store.Write(entity.Write{
		At: source.Ref(e.System, e.Name).At(), Value: body.Payload(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeOut(w, r, http.StatusCreated, out)
}

// overlaps reports why this registration may not stand beside the ones already installed, or ""
// if it provably may.
//
// Disjointness is PROVED, not assumed, and what cannot be proved is refused — so the message
// has to carry both claims, because the operator is the one who can tell whether they really
// are apart. `--allow-overlap` is the way past, and it is not free: two writers on one slice
// each tombstone what the other reflected.
//
// Scoped, which is load-bearing: a scoped source shadowing a baseline one is the feature, not a
// collision, and the store this reads is already the scope's.
func overlaps(r entity.Reader, e source.Entity) string {
	for _, other := range source.List(r) {
		if other.System == e.System && other.Name == e.Name {
			continue // a re-install of the same registration replaces it
		}
		// An UNDECLARED ownership is not an empty one, and the difference is the whole of this
		// check. source.Disjoint answers true for an empty list, and is right to: nothing
		// intersects nothing. But a registration that carries no claim has not said it writes
		// nothing — it has not said anything, so there is nothing to prove with, and a source
		// that turns out to write the same slice would be discovered by its neighbour's refs
		// disappearing. Set arithmetic stays in source.Disjoint; this is the install's policy.
		if len(e.Owns) == 0 || len(other.Owns) == 0 {
			return refusal(e, other)
		}
		ok, err := source.Disjoint(e.Owns, other.Owns)
		if err != nil {
			return fmt.Sprintf("source %q: %v", e.Name, err)
		}
		if ok {
			continue
		}
		return refusal(e, other)
	}
	return ""
}

// refusal is the one message this check has, and it carries BOTH claims: verse could not prove
// them apart, so the operator is the one who can tell whether they really are.
func refusal(e, other source.Entity) string {
	return fmt.Sprintf("refused: %s and the installed %s cannot be proved to write different "+
		"slices, and two sources on one slice each tombstone what the other reflected.\n"+
		"    this one owns: %s\n    %s owns: %s\n"+
		"--allow-overlap installs it anyway, with that cost",
		source.Ref(e.System, e.Name), source.Ref(other.System, other.Name),
		claims(e.Owns), source.Ref(other.System, other.Name), claims(other.Owns))
}

// claims renders an ownership list for a message, saying so when there is none — a source that
// claimed nothing is the commonest reason this refusal fires, and "owns: []" would not say why.
func claims(owns []string) string {
	if len(owns) == 0 {
		return "nothing (it claimed no ownership, so it can be proved apart from nothing)"
	}
	return strings.Join(owns, ", ")
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	probe := r.URL.Query().Get("probe") == "true"
	out := make([]map[string]any, 0)
	for _, e := range source.List(store) {
		rec := map[string]any{
			"ref": source.Ref(e.System, e.Name).String(), "name": e.Name, "system": e.System,
		}
		for k, v := range map[string]string{
			"url": e.URL, "dir": e.Dir, "scope": e.Scope,
			"lastReflect": e.LastReflect, "lastError": e.LastError,
		} {
			if v != "" {
				rec[k] = v
			}
		}
		if len(e.Owns) > 0 {
			rec["owns"] = e.Owns
		}
		// Asked for, never stored. A reachability column is as of now or it is a cache that can
		// be stale in the direction that costs — saying a source is well because it was well
		// when something last looked.
		if probe {
			reach, _, why := s.describe(r, e)
			rec["reach"] = reach.String()
			if why != nil {
				rec["why"] = why.Error()
			}
		}
		out = append(out, rec)
	}
	writeOut(w, r, http.StatusOK, out)
}

func (s *Server) getSource(w http.ResponseWriter, r *http.Request) {
	ref := source.Ref(r.PathValue("system"), r.PathValue("name"))
	e, ok, err := s.scopedStore(r).Get(ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeOut(w, r, http.StatusOK, entity.ToObj(e.Payload))
}

// checkSource asks one what it is, right now — including what it claims to own, which is what
// an operator reads before deciding whether `--allow-overlap` is honest.
func (s *Server) checkSource(w http.ResponseWriter, r *http.Request) {
	system, name := r.PathValue("system"), r.PathValue("name")
	e, ok := source.Find(s.scopedStore(r), system+":"+name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	reach, d, why := s.describe(r, e)
	out := map[string]any{
		"ref": source.Ref(system, name).String(), "reach": reach.String(),
	}
	if e.URL != "" {
		out["url"] = e.URL
	}
	if why != nil {
		out["why"] = why.Error()
	}
	// What it says NOW, beside what was recorded at install. Neither is merged into the other:
	// a claim that has drifted is a fact an operator should see as two values, not as one.
	if reach == source.IsSource {
		out["says"] = map[string]any{"system": d.System, "owns": d.Owns}
	}
	writeOut(w, r, http.StatusOK, out)
}

// removeSource takes the registration away. The fork — what happens to what it reflected — is
// docs/working/sources-plan.md step 5; today it removes the entity and nothing else.
func (s *Server) removeSource(w http.ResponseWriter, r *http.Request) {
	store := s.scopedStore(r)
	system, name := r.PathValue("system"), r.PathValue("name")
	ref := source.Ref(system, name)
	if _, ok := source.Find(store, system+":"+name); !ok {
		http.NotFound(w, r)
		return
	}
	if _, _, err := store.Write(entity.Write{At: ref.At(), Value: nil}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeOut(w, r, http.StatusOK, map[string]any{"removed": ref.String()})
}
