package versed

import (
	"io"
	"net/http"
	"testing"

	"github.com/signadot/verse/pkg/codec"
)

// decodeResp reads a response in the format it DECLARES, rather than assuming one.
//
// These tests used encoding/json directly, which was fine while JSON was the default and
// stopped being fine the moment it wasn't. Reading the declared format keeps them testing
// handler behaviour rather than the default rendering — and it means the same test body
// covers whichever format the baseline happens to be, which is what lets CI run the suite
// both ways.
func decodeResp(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type"))
	if !ok {
		f = codec.Default()
	}
	if err := codec.Unmarshal(body, v, f); err != nil {
		t.Fatalf("decoding a %v response: %v\n%s", f, err, body)
	}
}
