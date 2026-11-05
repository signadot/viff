package versed

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/signadot/verse/internal/resolver"
	"github.com/signadot/verse/pkg/api"
	"github.com/signadot/verse/pkg/entity"
	"github.com/signadot/verse/pkg/mirror"
)

// serves is the parties res answers for on the ask p (9ce3bva0, stage 4): where its match admits
// the ask, those of the ask's parties (mirror.Parties) whose `answer` grants name it. None for a
// resolver with no match, which answers for nobody by grant.
//
// Asked AS IT STANDS: a grant made or revoked since the ask was posed counts, and so does a match
// installed again.
func serves(store entity.Store, res resolver.Resolver, p entity.Payload) ([]entity.Ref, error) {
	if res.Match == nil {
		return nil, nil
	}
	subject := entity.Str(p, "subject")
	var about entity.Ref
	if subject != "" {
		if ref, err := entity.ParseAddr(subject); err == nil {
			about = ref
		}
	}
	if !res.Match.Admits(about, mirror.LabelsOf(p)) {
		return nil, nil
	}
	via := entity.Via(resolver.Ref(res.Name))
	var out []entity.Ref
	for _, party := range mirror.Parties(store, subject, mirror.GranteesTo(p)) {
		ok, err := entity.Holds(store, via, entity.Answer, party)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, party)
		}
	}
	return out, nil
}

// partyName is a party as a person reads it: the principal at `verse.for.<principal>`, or the
// place's address.
func partyName(party entity.Ref) string {
	if segs := party.Segs(); len(segs) == 3 && segs[0] == entity.VerseRoot && segs[1] == entity.ForSystem {
		return segs[2]
	}
	return party.String()
}

// servedAsks is GET /resolvers/{name}/asks: the waiting asks the resolver serves, and for whom. It
// is asked AS the resolver (api.ResolverHeader), which reads the asks of whoever granted it; its
// subscription is a watch on verse.ask made the same way, and this is what it serves of them now.
func (s *Server) servedAsks(w http.ResponseWriter, r *http.Request) {
	ref := resolver.Ref(r.PathValue("name"))
	if principalOf(r) != entity.Via(ref) {
		http.Error(w, fmt.Sprintf("what %s serves is read as it: name it in %s", ref, api.ResolverHeader),
			http.StatusForbidden)
		return
	}
	res, found := resolver.Find(s.scopedStore(r), ref.String())
	if !found {
		http.Error(w, fmt.Sprintf("no resolver %s", ref), http.StatusNotFound)
		return
	}
	ents, err := pendingQuestionsErr(s.scopedStore(r))
	if unreadable(w, r, err) {
		return
	}
	out := make([]api.ServedAsk, 0)
	for _, e := range ents {
		parties, err := serves(s.Store, res, e.Payload)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if len(parties) == 0 {
			continue
		}
		a := api.ServedAsk{
			ID: e.Ref.Name(), Edit: mirror.EditOf(e.Payload), Pose: strings.TrimSpace(entity.Str(e.Payload, "pose")),
			About: entity.Str(e.Payload, "subject"), Labels: mirror.LabelsOf(e.Payload),
		}
		for _, p := range parties {
			a.For = append(a.For, partyName(p))
		}
		out = append(out, a)
	}
	writeOut(w, r, http.StatusOK, out)
}

// answeringFor is the party an answer made as principal is for, where principal is a resolver
// with a match (acting): one it serves the ask p for, named by want, or the only one. A person
// party answers as themselves; a place answers as its owner at the moment of the answer. A
// resolver record with no match, standing from before resolvers watched asks, answers for
// nobody (not acting), and is held to what any principal is.
func (s *Server) answeringFor(principal string, p entity.Payload, want string) (party string, acting bool, err error) {
	g, perr := entity.ParseGrantee(principal)
	if perr != nil || g.Kind != entity.ViaKind {
		return "", false, nil
	}
	ref := entity.RefAt(g.Path...)
	if !resolver.IsResolver(ref) {
		return "", false, nil
	}
	res, found := resolver.Find(s.Store, ref.String())
	if !found || res.Match == nil {
		return "", false, nil
	}
	parties, err := serves(s.Store, res, p)
	if err != nil {
		return "", true, err
	}
	pick, err := pickParty(ref, parties, want)
	if err != nil {
		return "", true, err
	}
	return s.partyPrincipal(pick)
}

// servingParty is the party principal raises an onward ask for on the ask p: principal must be a
// resolver with a match that serves p for that party.
func (s *Server) servingParty(principal string, p entity.Payload, want string) (entity.Ref, error) {
	g, err := entity.ParseGrantee(principal)
	ref := entity.RefAt(g.Path...)
	if err != nil || g.Kind != entity.ViaKind || !resolver.IsResolver(ref) {
		return entity.Ref{}, fmt.Errorf("an onward ask is raised by the resolver serving the ask, made as it (%s)", api.ResolverHeader)
	}
	res, found := resolver.Find(s.Store, ref.String())
	if !found || res.Match == nil {
		return entity.Ref{}, fmt.Errorf("%s serves no ask: it has no match", ref)
	}
	parties, err := serves(s.Store, res, p)
	if err != nil {
		return entity.Ref{}, err
	}
	return pickParty(ref, parties, want)
}

// pickParty is the one of parties an act of resolver ref names in want, or the only one.
func pickParty(ref entity.Ref, parties []entity.Ref, want string) (entity.Ref, error) {
	var names []string
	for _, pt := range parties {
		names = append(names, partyName(pt))
	}
	if want != "" {
		if w, err := entity.ParseGrantee(want); err == nil && w.Kind != entity.ViaKind && w.Kind != entity.Everyone {
			want = w.String()
		}
	}
	switch {
	case len(parties) == 0:
		return entity.Ref{}, fmt.Errorf("%s answers for nobody on this ask: none of its parties granted it, "+
			"or its match does not admit it", ref)
	case want != "":
		i := slices.Index(names, want)
		if i < 0 {
			return entity.Ref{}, fmt.Errorf("%s does not answer for %s on this ask; it answers for %s",
				ref, want, strings.Join(names, ", "))
		}
		return parties[i], nil
	case len(parties) == 1:
		return parties[0], nil
	}
	return entity.Ref{}, fmt.Errorf("%s answers for %s on this ask; name one in `for`", ref, strings.Join(names, ", "))
}

// partyPrincipal is who a party's answer is given as: the person at `verse.for.<person>`, or a
// place's owner at this moment.
func (s *Server) partyPrincipal(pick entity.Ref) (string, bool, error) {
	if segs := pick.Segs(); len(segs) == 3 && segs[0] == entity.VerseRoot && segs[1] == entity.ForSystem {
		return segs[2], true, nil
	}
	owner, err := entity.OwnerOf(s.Store, pick)
	if err != nil {
		return "", true, err
	}
	if owner == "" {
		return "", true, fmt.Errorf("%s is nobody's, so nobody's answer is given for it", pick)
	}
	return owner, true, nil
}

// servedFor is the resolver serving the ask p for principal — as a person it names, or as the
// owner of the place an ask naming nobody is about — and "" where none does. Asked as grants and
// matches stand now.
func (s *Server) servedFor(principal string, p entity.Payload) (string, error) {
	if principal == "" {
		return "", nil
	}
	mine := entity.ForRef(principal).String()
	for _, res := range resolver.List(s.Store) {
		parties, err := serves(s.Store, res, p)
		if err != nil {
			return "", err
		}
		for _, party := range parties {
			if party.String() == mine {
				return resolver.Ref(res.Name).String(), nil
			}
			if segs := party.Segs(); len(segs) == 3 && segs[0] == entity.VerseRoot && segs[1] == entity.ForSystem {
				continue // somebody else's
			}
			if owner, err := entity.OwnerOf(s.Store, party); err != nil {
				return "", err
			} else if owner == principal {
				return resolver.Ref(res.Name).String(), nil
			}
		}
	}
	return "", nil
}

// askServed is GET /asks/{id}/served: which resolver serves the ask for the caller, if any. Asked
// of an ask the caller may read.
func (s *Server) askServed(w http.ResponseWriter, r *http.Request) {
	e, found, err := s.scopedStore(r).Get(mirror.AskRef(r.PathValue("id")))
	if refused(w, err) {
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	by, err := s.servedFor(principalOf(r), e.Payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeOut(w, r, http.StatusOK, api.AskServed{Resolver: by})
}
