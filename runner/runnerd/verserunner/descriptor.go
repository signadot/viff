package verserunner

import (
	"net/http"

	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/runner/runnerd"
)

// Descriptor is the `GET /runner` stub: what this is, how it applies control, what it can host,
// and what it can contain. Nothing consumes it yet — `verse runner add` still probes with
// `GET /tasks` — so it is here to be read by a person and to keep the shape honest while the
// contract half is designed.
func (r *Runner) Descriptor(w http.ResponseWriter, req *http.Request) {
	// Control is applied SYNCHRONOUSLY: a cancel that returns has been applied, which is what
	// verse assumes of anything it reaches over a url. A runner that instead queued control
	// would have to say `relayed` here — that field is the whole reason the descriptor is
	// owed (git-issue f6bmet8r).
	body := map[string]any{
		"runner":  "verse",
		"control": "synchronous",
		// Derived, not listed: `claude` is a config entry rather than a type in this program, so
		// a hard-coded list here would describe a build instead of a deployment.
		"drivers": r.driverNames(),
		// What THIS deployment can build: the sandbox it was started with, and the networks that
		// sandbox provides — not what the weakest one could. Reporting the host's floor
		// regardless of the sandbox would understate a bwrap runner in the one document an
		// operator reads before pointing a charter at it.
		"sandbox":      r.h.Sandbox.Name(),
		"network":      networksProvided(r.h.Sandbox),
		"environments": r.cfg.names(),
		// What this runner holds that the world does not. It belongs in the same document as the
		// capabilities because it is the other thing an operator needs before touching a runner:
		// removing one with undelivered work in it throws that work away.
		"undelivered": r.staged(),
	}
	out, err := codec.Marshal(body, codec.Tony)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", codec.MediaType(codec.Tony))
	_, _ = w.Write(out)
}

// driverNames is what this deployment can actually run: the drivers this program implements,
// plus every program its config names.
func (r *Runner) driverNames() []string {
	out := []string{"scripted", "calls"}
	if r.secrets.PortkeyAPIKey != "" {
		out = append(out, driverLLM)
	}
	if len(r.cfg.Agents) > 0 {
		out = append(out, agentNames(r.cfg.Agents)...)
	}
	return out
}

// networksProvided is every network mode a sandbox can build, for the descriptor.
func networksProvided(sb runnerd.Sandbox) []string {
	var out []string
	for _, mode := range runnerd.NetworkModes {
		if sb.Provides(runnerd.Network{Mode: mode}) == nil {
			out = append(out, mode)
		}
	}
	return out
}

// networkFor is the network an environment declares for its tasks. An egress allowlist IS the
// declaration of a restricted network, derived here rather than stated twice so the two cannot
// disagree.
func networkFor(env Environment) runnerd.Network {
	if len(env.Egress) > 0 {
		return runnerd.Network{Mode: runnerd.NetworkRestricted}
	}
	return runnerd.Network{Mode: runnerd.NetworkOpen}
}
