package api

import (
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
)

// VersionHeader is what each side of a verse conversation stamps its own version in — the
// client on every request, the server on every response.
//
// It exists because the wire contract is a moving one. The schema says what a verse looks
// like, and a client reading it from its own binary is reading the shapes THAT binary was
// built against; the verse it is talking to may have been built against others. Nothing else
// notices: a field that moved comes back missing, a route that changed answers 404, and both
// look like ordinary emptiness. This is the one place the two builds can say who they are.
const VersionHeader = "Verse-Version"

// PrincipalHeader carries who is asking: the principal every request from the api is made as,
// which the server stamps on every write the request makes (entity.Write.Principal), spelled
// with its kind — `user:<email>`, `agent:<name>`, `runner:<identity>` (entity.Grammar). The CLI
// fills it from its config's `user:` or `agent:`, and `--as` overrides it where a command takes
// one. It is
// self-asserted, exactly as `--as` is, and the server trusts it no harder — what it buys is
// that a write names a party, on the write, rather than in a field only some verbs carry.
//
// A request without it is unattributed: nothing is refused, and what it creates reads as its
// parent's, which is what an api used by hand from curl gets. One carrying a name the grammar
// does not read, or a name versed makes — `via:`, `system:` — is refused 400.
const PrincipalHeader = "Verse-Principal"

// Version is this binary's own verse version, read from its build info — the version that
// actually shipped, not one written down somewhere and left behind. It is "(devel)" for a
// build from a checkout, which is what `go run ./cmd/verse` and every test binary report, and
// "" when the build info cannot be read at all.
func Version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return bi.Main.Version
}

// Skew is how far apart two verse versions are.
type Skew int

const (
	// SameVersion is no distance worth mentioning: identical versions, or a difference only
	// in the patch component, which is what a compatible release changes.
	SameVersion Skew = iota
	// VersionDrift is a difference that may show: features one side has and the other does
	// not. It is worth a word to the operator and nothing more.
	VersionDrift
	// VersionIncompatible is a difference across the component that promises compatibility.
	// The two are not expected to understand each other, so the request does not go through
	// — a write half-understood is worse than a write refused.
	VersionIncompatible
)

// VersionSkew reports how far apart a client and a server are, and says it in one line. The
// line names BOTH versions, always: the failure a skew produces is a missing field or a 404,
// neither of which says "your verse is older than the one you are talking to".
//
// The rule is semver's, taken plainly. A different MAJOR is incompatible — that is the whole
// content of the number. A different MINOR is drift: one side has features the other does not,
// which is worth saying and not worth refusing over. A different PATCH is nothing, because a
// patch release that changed the wire would not be one.
//
// Read plainly, the major rule is DORMANT below v1: 0.1 and 0.2 are a minor apart and will
// warn, though semver says a 0.x minor may break anything. That is the deliberate choice. The
// alternative — treating the leading nonzero component as the compatibility promise, which is
// how Cargo reads 0.x — makes v0.0.1 and v0.0.2 refuse to talk to each other, and v0.0.x is
// exactly where a project in this ecosystem starts (go-tony is at v0.0.109). A rule that
// bricks a patch bump costs more than one that only warns about an early minor.
//
// A version that does not parse — "(devel)", or empty because the build info would not read —
// cannot be compared, so any DIFFERENCE is drift and nothing is ever incompatible. That is the
// live case today and it is deliberately the mild one: two checkouts of different vintages are
// the normal way verse is developed, and refusing to talk would make the tool unusable long
// before a tag exists to make the judgement on.
func VersionSkew(client, server string) (Skew, string) {
	if client == server {
		return SameVersion, ""
	}
	c, cok := parseVersion(client)
	s, sok := parseVersion(server)
	switch {
	case !cok || !sok:
		return VersionDrift, fmt.Sprintf("client %s, server %s", name(client), name(server))
	case c[0] != s[0]:
		return VersionIncompatible, fmt.Sprintf(
			"client %s and server %s are incompatible releases", client, server)
	case c[1] != s[1]:
		return VersionDrift, fmt.Sprintf(
			"client %s, server %s — one has features the other does not", client, server)
	}
	return SameVersion, ""
}

// parseVersion reads vMAJOR.MINOR.PATCH, ignoring any pre-release or build suffix. Anything
// else — "(devel)", "", a branch name — does not parse, which is a fact about the version
// rather than an error: see VersionSkew for what is done with it.
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	s := strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// name is how a version reads in a message when it may be nothing at all.
func name(v string) string {
	if v == "" {
		return "an unreadable version"
	}
	return v
}
