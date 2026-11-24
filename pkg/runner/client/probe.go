package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.viff.dev/viff/pkg/runner"
	"go.viff.dev/viff/pkg/task"
)

// Reach is what a probe found at a url: a runner, something else answering, or nothing. A
// fourth answer, Unverified, is a caller's to give: a runner that answered and did not prove it
// is the one registered (viffd's handshake), which no probe decides on its own.
type Reach int

const (
	IsRunner Reach = iota
	NotRunner
	Unreachable
	Unverified
)

func (r Reach) String() string {
	switch r {
	case IsRunner:
		return "a runner"
	case NotRunner:
		return "not a runner"
	case Unverified:
		return "not verified"
	default:
		return "unreachable"
	}
}

// Probe asks a url what it is: a runner is what describes itself (GET /describe), and the
// description is what the runner offers. A url that answers and does not describe itself is
// not a runner; one nothing answers is unreachable, which may be the ordinary state of a
// runner not yet deployed.
func Probe(baseURL string) (Reach, runner.Description, error) {
	reach, d, _, err := ProbeOver(baseURL, nil)
	return reach, d, err
}

// ProbeOver is Probe over a transport of the caller's choosing (nil is the default), answering
// also the token the runner presented in runner.TokenHeader, "" where it presented none. The
// transport is how viff carries its own token to the runner (Bearer); the header is the
// runner's answer to it.
func ProbeOver(baseURL string, tr http.RoundTripper) (Reach, runner.Description, string, error) {
	d, token, err := DescribeOver(baseURL, tr)
	switch {
	case err == nil:
		return IsRunner, d, token, nil
	case errors.Is(err, task.ErrUnsupported):
		return NotRunner, runner.Description{}, "", err
	default:
		return Unreachable, runner.Description{}, "", err
	}
}

// Describe reads what a runner offers, GET /describe. A url that does not serve it, or serves
// something that is not a description, is task.ErrUnsupported; one that does not answer is
// task.ErrUnreachable.
func Describe(baseURL string) (runner.Description, error) {
	d, _, err := DescribeOver(baseURL, nil)
	return d, err
}

// DescribeOver is Describe over a transport, answering the runner's token too (ProbeOver). A
// runner that refused the request (401, 403) is unreachable rather than not a runner: it is one,
// and it did not take this viff for its own, which deploying viff's token fixes.
func DescribeOver(baseURL string, tr http.RoundTripper) (runner.Description, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/describe", nil)
	if err != nil {
		return runner.Description{}, "", fmt.Errorf("%w: %s is not a usable runner url: %w", task.ErrUnreachable, baseURL, err)
	}
	cl := &http.Client{Transport: tr}
	resp, err := cl.Do(req)
	if err != nil {
		return runner.Description{}, "", fmt.Errorf("%w: %s: %w", task.ErrUnreachable, baseURL, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		// The runner's own words say which of the two it was: a token for another runner, or a
		// viff it does not know.
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return runner.Description{}, "", fmt.Errorf("%w: %s refused this viff (GET /describe: %s: %s): it serves "+
			"its own viff alone, and this viff presented no token it takes", task.ErrUnreachable, baseURL, resp.Status,
			strings.TrimSpace(string(said)))
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		// A runner that cannot answer right now (its issuer unread, its token file gone) is a
		// runner, and asking again may find it: unreachable, never not a runner.
		return runner.Description{}, "", fmt.Errorf("%w: %s could not answer (GET /describe: %s)",
			task.ErrUnreachable, baseURL, resp.Status)
	default:
		return runner.Description{}, "", fmt.Errorf("%w: %s does not describe itself (GET /describe: %s)",
			task.ErrUnsupported, baseURL, resp.Status)
	}
	var d runner.Description
	if err := decodeResp(resp, &d); err != nil {
		return runner.Description{}, "", fmt.Errorf("%w: %s answered an undecodable description: %w",
			task.ErrUnsupported, baseURL, err)
	}
	return d, resp.Header.Get(runner.TokenHeader), nil
}
