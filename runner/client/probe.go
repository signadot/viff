package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/task"
)

// Reach is what a probe found at a url: a runner, something else answering, or nothing.
type Reach int

const (
	IsRunner Reach = iota
	NotRunner
	Unreachable
)

func (r Reach) String() string {
	switch r {
	case IsRunner:
		return "a runner"
	case NotRunner:
		return "not a runner"
	default:
		return "unreachable"
	}
}

// Probe asks a url what it is: a runner is what describes itself (GET /describe), and the
// description is what the runner offers. A url that answers and does not describe itself is
// not a runner; one nothing answers is unreachable, which may be the ordinary state of a
// runner not yet deployed.
func Probe(baseURL string) (Reach, runner.Description, error) {
	d, err := Describe(baseURL)
	switch {
	case err == nil:
		return IsRunner, d, nil
	case errors.Is(err, task.ErrUnsupported):
		return NotRunner, runner.Description{}, err
	default:
		return Unreachable, runner.Description{}, err
	}
}

// Describe reads what a runner offers, GET /describe. A url that does not serve it, or serves
// something that is not a description, is task.ErrUnsupported; one that does not answer is
// task.ErrUnreachable.
func Describe(baseURL string) (runner.Description, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/describe", nil)
	if err != nil {
		return runner.Description{}, fmt.Errorf("%w: %s is not a usable runner url: %w", task.ErrUnreachable, baseURL, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return runner.Description{}, fmt.Errorf("%w: %s: %w", task.ErrUnreachable, baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return runner.Description{}, fmt.Errorf("%w: %s does not describe itself (GET /describe: %s)",
			task.ErrUnsupported, baseURL, resp.Status)
	}
	var d runner.Description
	if err := decodeResp(resp, &d); err != nil {
		return runner.Description{}, fmt.Errorf("%w: %s answered an undecodable description: %w",
			task.ErrUnsupported, baseURL, err)
	}
	return d, nil
}
