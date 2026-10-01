package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// What a request is authorized with: a token somebody issued (a PAT), or a GitHub App's
// installation, which mints its own (git-issue n69fce9dh12krpecqsn0).
//
// AN APP, because what the io does back to GitHub is done AS whoever the credential is. With a
// person's token, a review of a pull request that person opened is refused and a comment reads as
// theirs; with an app it is `<slug>[bot]`, which is what the review environment needs.
type credential interface {
	// authorization is the Authorization header's value for a request made now.
	authorization(ctx context.Context) (string, error)
}

// bearer is a token somebody issued.
type bearer string

func (b bearer) authorization(context.Context) (string, error) { return "Bearer " + string(b), nil }

// AppAuth is a GitHub App's installation: the app's id, the installation's, and the app's private
// key, PEM-encoded as GitHub hands it over.
type AppAuth struct {
	AppID          string
	InstallationID string
	PrivateKey     []byte
}

// installation mints installation tokens: a JWT signed with the app's key, exchanged at
// /app/installations/<id>/access_tokens for a token that lasts an hour. One is kept until shortly
// before it expires, so a pass costs no exchange.
type installation struct {
	appID, id string
	key       *rsa.PrivateKey
	base      string
	cl        *http.Client
	now       func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// refreshBefore is how long before its expiry a token is replaced: long enough that a request
// started with it does not reach GitHub after it lapsed.
const refreshBefore = 5 * time.Minute

// newInstallation checks the app's credentials as far as can be without GitHub, so a malformed
// key is a process that does not start rather than a first pass that fails.
func newInstallation(a AppAuth, base string, cl *http.Client) (*installation, error) {
	if a.AppID == "" || a.InstallationID == "" || len(a.PrivateKey) == 0 {
		return nil, errors.New("github: an app needs its id, its installation's id and its private key")
	}
	key, err := parseKey(a.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("github: app %s: %w", a.AppID, err)
	}
	return &installation{appID: a.AppID, id: a.InstallationID, key: key, base: base, cl: cl, now: time.Now}, nil
}

// parseKey reads the private key GitHub generates (PKCS#1), or the same key re-encoded as PKCS#8.
func parseKey(p []byte) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode(p)
	if blk == nil {
		return nil, errors.New("the private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the private key is neither PKCS#1 nor PKCS#8: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the private key is not an RSA key, which is what GitHub signs with")
	}
	return rk, nil
}

func (i *installation) authorization(ctx context.Context) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.token == "" || !i.now().Add(refreshBefore).Before(i.expires) {
		if err := i.mint(ctx); err != nil {
			return "", err
		}
	}
	return "Bearer " + i.token, nil
}

// mint exchanges a fresh JWT for an installation token.
func (i *installation) mint(ctx context.Context) error {
	jwt, err := i.jwt()
	if err != nil {
		return err
	}
	url := i.base + "/app/installations/" + i.id + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := i.cl.Do(req)
	if err != nil {
		return fmt.Errorf("github: minting a token for app %s's installation %s: %w", i.appID, i.id, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("github: minting a token for app %s's installation %s: %s: %s",
			i.appID, i.id, resp.Status, strings.TrimSpace(string(body)))
	}
	var got struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &got); err != nil || got.Token == "" {
		return fmt.Errorf("github: minting a token for app %s's installation %s: no token in the answer",
			i.appID, i.id)
	}
	i.token, i.expires = got.Token, got.ExpiresAt
	return nil
}

// jwt is the app's own assertion, RS256 over {iat, exp, iss}. Issued a minute in the past, as
// GitHub advises against clock drift, and good for nine of the ten minutes GitHub allows.
func (i *installation) jwt() (string, error) {
	now := i.now()
	enc := base64.RawURLEncoding
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": i.appID,
	})
	signed := enc.EncodeToString(head) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("github: signing app %s's assertion: %w", i.appID, err)
	}
	return signed + "." + enc.EncodeToString(sig), nil
}
