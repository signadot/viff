package main

import "testing"

// brokerFace advertises a dialable URL: wildcard/empty hosts become localhost (dev), an
// explicit host is preserved (deployment), and the scheme is http.
func TestBrokerFace(t *testing.T) {
	cases := map[string]string{
		":9090":           "http://localhost:9090",
		"0.0.0.0:9090":    "http://localhost:9090",
		"[::]:9090":       "http://localhost:9090",
		"verse.test:9090": "http://verse.test:9090",
		"127.0.0.1:9090":  "http://127.0.0.1:9090",
	}
	for in, want := range cases {
		if got := brokerFace(in); got != want {
			t.Errorf("brokerFace(%q) = %q, want %q", in, got, want)
		}
	}
}
