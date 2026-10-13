// Package verserunner is the `verse` runner: it offers verse's own agent (package agent) and
// scripts. `runnerd verse` serves it.
//
// It runs on today's runner API, where a launch names a driver: `llm` is the verse agent and
// `scripted` is a script. The spawn spec's doer replaces the driver later.
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
	EnvPortkeyAPIKey  = "PORTKEY_API_KEY"
	EnvPortkeyBaseURL = "PORTKEY_BASE_URL"
)

// Config is the verse runner's own configuration, beside runnerd.Config in the same file.
type Config struct {
	Models Models `tony:"field=models,omitzero"`
	// Annotations is the annotations a spawn may set, with the type of their value.
	Annotations map[string]string `tony:"field=annotations,omitzero"`
}

// Models is how the verse agent reaches its model.
type Models struct {
	Portkey Portkey `tony:"field=portkey,omitzero"`
	// Model is the agent's model when a spawn doesn't pick one.
	Model string `tony:"field=model,omitzero"`
}

// Portkey is the Portkey route. Its account key is read from PORTKEY_API_KEY.
type Portkey struct {
	// VirtualKey is used when a spawn doesn't pick one.
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

// Check validates the configuration.
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
	return nil
}

// accepts reports whether the configuration accepts an annotation.
func (c Config) accepts(annotation string) bool {
	_, ok := c.Annotations[annotation]
	return ok
}
