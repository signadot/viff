// Package verserunner is the `verse` runner: it offers verse's own agent (package agent), scripts,
// and the programs its configuration names under `agents:`; and when its configuration declares
// environments, it gives each task a workspace of git checkouts, a sandbox with the network the
// environment declares, and a push the spawn's repositories permit. `runnerd verse` serves
// it.
//
// It runs on today's runner API, where a launch names a driver: `llm` is the verse agent,
// `scripted` a script, `calls` a list of calls, and any other name a program under `agents:`.
// The spawn spec's doer replaces the driver later.
package verserunner

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/signadot/verse/runner/runnerd"
)

// AnnotationVirtualKey lets a spawn pick the Portkey virtual key its run uses: a routing label
// (route, budget), not a secret. The runner holds the Portkey account key.
const AnnotationVirtualKey = "verse.portkey/virtual-key"

// annotations is every annotation the verse runner knows, with the type of its value. A
// configuration may accept any of them.
var annotations = map[string]string{AnnotationVirtualKey: "string"}

// The environment variables the runner reads its secrets from.
const (
	EnvPortkeyAPIKey     = "PORTKEY_API_KEY"
	EnvPortkeyBaseURL    = "PORTKEY_BASE_URL"
	EnvPortkeyVirtualKey = "PORTKEY_VIRTUAL_KEY"
)

// Config is the verse runner's own configuration, beside runnerd.Config in the same file.
//
// It is NOT an entity: verse installs a runner as a url and nothing else, deliberately, so what a
// runner can be asked for is the runner's to declare and the operator's to deploy.
type Config struct {
	Models Models `tony:"field=models,omitzero"`
	// Annotations is the annotations a spawn may set, with the type of their value.
	Annotations map[string]string `tony:"field=annotations,omitzero"`

	// Environments are the workspaces this runner can build. A spawn names one
	// (`environment:` on a charter, `--environment` on `verse task run`) and verse carries the
	// name without resolving it — the same indirection `driver:` already is. A runner with none
	// gives each task a fresh directory and no repository.
	Environments []Environment `tony:"field=environments,omitzero"`
	// Default is the environment a spawn that named none gets. Empty means a spawn must name
	// one, which is the safer of the two and is what an operator serving several repositories
	// wants.
	Default string `tony:"field=default,omitzero"`
	// Agents are the external programs this runner can drive, by the name a spawn writes in
	// `driver:`. `claude` is an entry here rather than a type in the code, which is the point:
	// nothing in this runner knows what a coding agent is, only how to run a program and read
	// what it said.
	Agents map[string]Command `tony:"field=agents,omitzero"`
}

// Models is how the verse agent reaches its model.
type Models struct {
	Portkey Portkey `tony:"field=portkey,omitzero"`
	// Model is the agent's model when a spawn doesn't pick one.
	Model string `tony:"field=model,omitzero"`
	// BaseURL is the model endpoint, an OpenAI-compatible one. Empty is PORTKEY_BASE_URL, and
	// without that Portkey's own.
	BaseURL string `tony:"field=baseUrl,omitzero"`
}

// Portkey is the Portkey route. Its account key is read from PORTKEY_API_KEY.
type Portkey struct {
	// VirtualKey is used when a spawn doesn't pick one. Empty is PORTKEY_VIRTUAL_KEY.
	VirtualKey string `tony:"field=virtualKey,omitzero"`
}

// Load reads the verse runner's configuration file, and its secrets from the environment.
func Load(path string) (runnerd.Config, Config, error) {
	var c Config
	rc, err := runnerd.Load(path, &c)
	if err != nil {
		return rc, c, err
	}
	if err := c.Check(); err != nil {
		return rc, c, fmt.Errorf("%s: %w", path, err)
	}
	return rc, c, nil
}

// Check validates the configuration, and resolves what is relative in it: a local repository's
// path is made absolute, since the side that uses it is not the side that wrote it. Every check
// here is about something that cannot be discovered later: a workspace with no repo, two
// workspaces with one name, a default naming nothing.
func (c *Config) Check() error {
	for name, typ := range c.Annotations {
		want, ok := annotations[name]
		if !ok {
			return fmt.Errorf("annotations: the verse runner has no annotation %q (it has: %s)",
				name, strings.Join(slices.Sorted(maps.Keys(annotations)), ", "))
		}
		if typ != want {
			return fmt.Errorf("annotations: %s takes a %s, not a %s", name, want, typ)
		}
	}
	return c.checkEnvironments()
}

// accepts reports whether the configuration accepts an annotation.
func (c Config) accepts(annotation string) bool {
	_, ok := c.Annotations[annotation]
	return ok
}
