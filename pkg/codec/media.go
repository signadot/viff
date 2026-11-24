package codec

import (
	"sort"
	"strconv"
	"strings"
)

// The media types viff emits.
//
// go-tony registers none, so viff picks. application/ rather than text/ because tony is
// a structured serialization consumed by a parser, which is what RFC 6838 puts there, and
// because application/json (RFC 8259) and application/yaml (RFC 9512) are the direct
// precedents — tony belongs in that family.
//
// NOT application/yaml for tony, even though tony is a YAML dialect: a tony document with
// tags is not general YAML, so labelling it yaml hands a YAML parser something it will
// mis-read rather than reject.
//
// Strictly, an unregistered subtype should be x- or vnd.-prefixed. application/yaml's
// history shows the unprefixed name is what eventually gets registered, and RFC 6648
// deprecates minting new x- types, so viff emits the plain name and merely ACCEPTS the
// alternatives.
const (
	MediaTony = "application/tony"
	MediaJSON = "application/json"
	MediaYAML = "application/yaml"
)

// mediaAliases are accepted on input. Emitting is always via MediaType.
//
// application/json is here, and stays here: a request that labels its BODY as JSON is
// stating a fact about bytes viff can read. It is Negotiate — the response side — that
// will not hand JSON back.
var mediaAliases = map[string]Format{
	MediaTony:                       Tony,
	"application/x-tony":            Tony,
	"application/vnd.signadot.tony": Tony,
	"text/tony":                     Tony,
	"text/x-tony":                   Tony,

	MediaJSON:   JSON,
	"text/json": JSON,

	MediaYAML:            YAML,
	"application/x-yaml": YAML,
	"text/yaml":          YAML,
	"text/x-yaml":        YAML,
}

// MediaType is the media type viff emits for a format.
func MediaType(f Format) string {
	switch f {
	case JSON:
		return MediaJSON
	case YAML:
		return MediaYAML
	default:
		return MediaTony
	}
}

// FormatForMedia resolves one media type — parameters (";charset=utf-8") tolerated but
// never required — reporting whether it named a format viff knows.
func FormatForMedia(ct string) (Format, bool) {
	name, _, _ := strings.Cut(ct, ";")
	f, ok := mediaAliases[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Default(), false
	}
	return f, true
}

// Negotiate picks a format from an Accept header, honoring q-values and falling back
// when the header is absent, is */*, or names nothing viff can produce. ok is false when
// the client named ONLY things viff cannot render — the 406 case; see ResponseFormat.
//
// q-values matter because the natural thing for a client to send is a preference rather
// than a demand — "tony, or JSON if you must" is `application/tony, application/json;q=0.5`,
// and taking the textually-first entry would get that backwards as often as not.
//
// An unwritable type (application/json) is dropped from the candidates rather than
// resolved to something else. That is what makes the q=0.5 example above still work — the
// client gets its first choice — while `Accept: application/json` alone gets an honest
// refusal instead of a tony body under a JSON content type.
func Negotiate(accept string, fallback Format) (Format, bool) {
	type pref struct {
		f Format
		q float64
		i int
	}
	var prefs []pref
	var refused bool
	for i, part := range strings.Split(accept, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, params, _ := strings.Cut(part, ";")
		name = strings.ToLower(strings.TrimSpace(name))
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(p, "=")
			if ok && strings.ToLower(strings.TrimSpace(k)) == "q" {
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = parsed
				}
			}
		}
		if q <= 0 { // q=0 is an explicit refusal
			continue
		}
		f, ok := mediaAliases[name]
		if !ok {
			// */* (and type/*) express no preference — the fallback IS the preference.
			if name == "*/*" || strings.HasSuffix(name, "/*") {
				f = fallback
			} else {
				continue
			}
		}
		if !Writable(f) {
			// A type viff knows and will not render. Remembered, not merely skipped:
			// asking for json is a different mistake from asking for text/html, and only
			// the first one deserves an explanation.
			refused = true
			continue
		}
		prefs = append(prefs, pref{f: f, q: q, i: i})
	}
	if len(prefs) == 0 {
		// Nothing acceptable. If the client named json, that is why — say so. Anything
		// else (a header full of types viff never heard of) keeps the old lenient
		// behaviour: serve the fallback rather than argue.
		return fallback, !refused
	}
	// Highest q wins; ties go to the order the client wrote them.
	sort.SliceStable(prefs, func(a, b int) bool {
		if prefs[a].q != prefs[b].q {
			return prefs[a].q > prefs[b].q
		}
		return prefs[a].i < prefs[b].i
	})
	return prefs[0].f, true
}
