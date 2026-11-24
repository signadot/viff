package viff

import (
	"cmp"
	"fmt"
	"net/url"
	"os"
)

// The variables the runner reads the viff agent's credentials from, as codex reads its key
// (codex.EnvAPIKey) and claude its credential (claude.EnvAPIKey): Portkey's account key, its
// endpoint, and the virtual key a run uses where neither its launch nor its runner's
// configuration names one.
const (
	EnvAPIKey     = "PORTKEY_API_KEY"
	EnvBaseURL    = "PORTKEY_BASE_URL"
	EnvVirtualKey = "PORTKEY_VIRTUAL_KEY"
)

// AnnotationVirtualKey lets a launch pick the Portkey virtual key its run uses: a routing label
// (route, budget), not a secret. The runner holds the Portkey account key.
const AnnotationVirtualKey = "viff.portkey/virtual-key"

// Creds is what the viff agent calls its model with.
type Creds struct {
	APIKey, BaseURL, VirtualKey string
}

// Key reads the agent's credentials from this process's environment: the runner's, where the
// deployment put them.
func Key() Creds {
	return Creds{APIKey: os.Getenv(EnvAPIKey), BaseURL: os.Getenv(EnvBaseURL), VirtualKey: os.Getenv(EnvVirtualKey)}
}

// ModelHost is where the agent calls its model, at base ("" is DefaultBaseURL): what a runner lets
// its process reach whatever the task's network. A base that names no host is refused: no network
// could let the agent reach it.
func ModelHost(base string) (string, error) {
	base = cmp.Or(base, DefaultBaseURL)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%s: %q is not a url with a host, and the agent's model is called there", EnvBaseURL, base)
	}
	return u.Host, nil
}
