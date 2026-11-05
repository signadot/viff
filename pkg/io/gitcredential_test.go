package io_test

import (
	"context"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	verseio "github.com/signadot/verse/pkg/io"
)

// gitCredential is what git's own credential machinery answers for github.com, as a fetch or a
// push would ask it.
func gitCredential(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "credential", "fill")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git credential fill: %v", err)
	}
	return string(out)
}

// A token given to the git commands of this process is what git presents, as x-access-token, in
// place of any helper set before it; and a new token is presented once it is minted.
func TestGitPresentsTheTokenItIsGiven(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// A helper the deployment set before: a person's token, which must not be presented.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", `!f() { echo username=someone; echo password=a-pat; }; f`)
	t.Setenv("GIT_CONFIG_KEY_1", "")
	t.Setenv("GIT_CONFIG_VALUE_1", "")
	t.Setenv("GIT_CONFIG_KEY_2", "")
	t.Setenv("GIT_CONFIG_VALUE_2", "")
	var n atomic.Int32
	token := func(context.Context) (string, error) {
		return "ghs_token" + string(rune('0'+n.Add(1))), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := verseio.UseGitToken(ctx, token, t.TempDir(), 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	got := gitCredential(t)
	if !strings.Contains(got, "username=x-access-token") || !strings.Contains(got, "password=ghs_token") || strings.Contains(got, "a-pat") {
		t.Fatalf("git presents %q, want the token as x-access-token and not the earlier helper's", got)
	}
	first := got
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && gitCredential(t) == first {
		time.Sleep(20 * time.Millisecond)
	}
	if gitCredential(t) == first {
		t.Error("a new token was minted and git still presents the first")
	}
}
