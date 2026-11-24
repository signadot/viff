package viffd

import (
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
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Viff as an issuer: a signing key, a discovery document and a key set, and nothing else of an
// identity provider (git-issue hfh7km3th12ksd6nqsn0, step 2). There are no login flows. What it
// signs is a short-lived token for a task whose gate passed, so that a system viff does not own
// — a cloud's IAM, Vault, Kubernetes — can be told to accept only work a gate let through, and
// checks that for itself against the keys published here.
//
// THE TWO DOCUMENTS ARE READS THAT NAME NOBODY. A relying party is no principal of this viff instance:
// it fetches `<url>/.well-known/openid-configuration`, follows `jwks_uri`, and verifies a
// signature. Both are OIDC's wire format, which is JSON by specification, and the JSON stops at
// this file.
//
// THE KEY'S ID IS ITS THUMBPRINT (RFC 7638), so a key names itself: the same key has the same
// `kid` wherever it is loaded, and a rotated key cannot be mistaken for the one before it.

const (
	issuerDiscoveryPath = "/.well-known/openid-configuration"
	issuerKeysPath      = "/.well-known/jwks.json"
)

// issuer signs tokens as this viff instance and publishes what verifies them.
type issuer struct {
	// url is the issuer as its relying parties reach it: every token's `iss`, and where the
	// discovery document is found.
	url string
	key *rsa.PrivateKey
	kid string
	// from is where the key is from, as status says it (api.Issuer.Key).
	from string
}

// SetIssuer makes this viff instance an issuer at url, signing with key, which is from where from says
// (api.Issuer.Key). It is called before the server serves. url is the viff instance's address as a
// relying party reaches it, which is the deployment's to say: a viff instance behind a proxy does not
// know it.
func (s *Server) SetIssuer(issuerURL string, key *rsa.PrivateKey, from string) error {
	u, err := url.Parse(issuerURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("the issuer's url %q is not one a relying party can fetch from: it is "+
			"https://<host>[/<path>]", issuerURL)
	}
	if key == nil {
		return errors.New("an issuer signs with a key, and none was given")
	}
	if key.N.BitLen() < 2048 {
		return fmt.Errorf("the issuer's key is %d bits, and RS256 wants 2048 or more", key.N.BitLen())
	}
	s.issuer = &issuer{url: strings.TrimSuffix(issuerURL, "/"), key: key, kid: thumbprint(&key.PublicKey), from: from}
	return nil
}

// IssuerKeyIn is the signing key kept in dir as issuer.pem, made there, readable by its owner
// alone, where there is none yet: the key `viff up` signs with where its config names no issuer,
// kept with the data so a restart signs as the same issuer. path is where it is kept.
func IssuerKeyIn(dir string) (key *rsa.PrivateKey, path string, _ error) {
	path = filepath.Join(dir, "issuer.pem")
	if _, err := os.Stat(path); err == nil {
		key, err := ReadIssuerKey(path)
		return key, path, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, path, err
	}
	key, err := NewIssuerKey()
	if err != nil {
		return nil, path, err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, path, err
	}
	return key, path, nil
}

// NewIssuerKey is a fresh signing key, for an issuer that keeps none.
func NewIssuerKey() (*rsa.PrivateKey, error) { return rsa.GenerateKey(rand.Reader, 2048) }

// ReadIssuerKey reads an issuer's signing key from a PEM file: an RSA private key, PKCS#1 or
// PKCS#8.
func ReadIssuerKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(data)
	if blk == nil {
		return nil, fmt.Errorf("%s is not PEM", path)
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s is neither a PKCS#1 nor a PKCS#8 private key: %w", path, err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s is not an RSA key, which is what an issuer signs with (RS256)", path)
	}
	return rk, nil
}

// thumbprint is a public key's JWK thumbprint (RFC 7638): the SHA-256 of the key's required
// members, in their lexical order with no whitespace, base64url-encoded.
func thumbprint(pub *rsa.PublicKey) string {
	e, n := jwkNumbers(pub)
	sum := sha256.Sum256([]byte(`{"e":"` + e + `","kty":"RSA","n":"` + n + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// jwkNumbers is a public key's exponent and modulus as a JWK spells them.
func jwkNumbers(pub *rsa.PublicKey) (e, n string) {
	enc := base64.RawURLEncoding
	return enc.EncodeToString(big.NewInt(int64(pub.E)).Bytes()), enc.EncodeToString(pub.N.Bytes())
}

// sign is a JWT of claims, signed RS256 with the issuer's key and naming it by `kid`. The claims
// are the caller's whole: what a token says is decided where it is minted.
func (i *issuer) sign(claims map[string]any) (string, error) {
	enc := base64.RawURLEncoding
	head, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": i.kid})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("the token's claims: %w", err)
	}
	signed := enc.EncodeToString(head) + "." + enc.EncodeToString(body)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("signing as %s: %w", i.url, err)
	}
	return signed + "." + enc.EncodeToString(sig), nil
}

// serveIssuerDiscovery is the discovery document: who the issuer is, where its keys are, and how
// it signs. It names no authorization or token endpoint, since nobody logs in here.
func (s *Server) serveIssuerDiscovery(w http.ResponseWriter, r *http.Request) {
	i := s.issuer
	if i == nil {
		http.Error(w, "this viff instance issues no tokens", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{
		"issuer":                                i.url,
		"jwks_uri":                              i.url + issuerKeysPath,
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

// serveIssuerKeys is the key set a relying party verifies a token against.
func (s *Server) serveIssuerKeys(w http.ResponseWriter, r *http.Request) {
	i := s.issuer
	if i == nil {
		http.Error(w, "this viff instance issues no tokens", http.StatusNotFound)
		return
	}
	e, n := jwkNumbers(&i.key.PublicKey)
	writeJSON(w, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": i.kid, "n": n, "e": e,
	}}})
}

// writeJSON answers with v as JSON, whatever format the request asked for: a relying party reads
// these documents as the specification writes them.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
