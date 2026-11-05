package io

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// UseGitToken makes a token the only credential the git commands of this process present over
// https, as `x-access-token`: how the git io fetches and pushes as a GitHub App's installation
// rather than with a person's token (git-issue dzxn44gw).
//
// The token is kept in a file under dir, mode 0600, rewritten whenever token answers a new one,
// and a credential helper reads it: git asks for a credential per command, and the token turns
// over every hour, so the helper reads the file afresh each time rather than being handed a value.
// It is checked every refresh while ctx lives; a failure to mint is logged and the last token is
// kept until it lapses, since GitHub being briefly unreachable should not empty the file.
//
// THE ONLY HELPER: the list of helpers git has from the environment and its config is reset
// first, so a person's token mounted beside the app (the deployment's `verse-git` Secret) is not
// tried ahead of it. Where a deployment gives both, the app is used, as it is for the github io.
//
// It sets GIT_CONFIG_* in this process's environment, which every git command it runs inherits,
// so it is called once, at start, before the first clone.
func UseGitToken(ctx context.Context, token func(context.Context) (string, error), dir string, refresh time.Duration) error {
	path := filepath.Join(dir, "git-token")
	write := func() error {
		t, err := token(ctx)
		if err != nil {
			return err
		}
		tmp := path + ".new"
		if err := os.WriteFile(tmp, []byte(t+"\n"), 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := write(); err != nil {
		return fmt.Errorf("git credential: %w", err)
	}
	n := 0
	if v, err := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT")); err == nil && v > 0 {
		n = v
	}
	helper := fmt.Sprintf(`!f() { if [ "$1" = get ]; then echo username=x-access-token; echo "password=$(cat %q)"; fi; }; f`, path)
	for _, kv := range [][2]string{{"credential.helper", ""}, {"credential.helper", helper}} {
		if err := os.Setenv("GIT_CONFIG_KEY_"+strconv.Itoa(n), kv[0]); err != nil {
			return err
		}
		if err := os.Setenv("GIT_CONFIG_VALUE_"+strconv.Itoa(n), kv[1]); err != nil {
			return err
		}
		n++
	}
	if err := os.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(n)); err != nil {
		return err
	}
	go func() {
		tick := time.NewTicker(refresh)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := write(); err != nil {
					log.Printf("git credential: keeping the last token: %v", err)
				}
			}
		}
	}()
	return nil
}
