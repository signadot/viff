package trigger

import (
	"testing"

	"github.com/signadot/tony-format/go-tony/ir"

	"go.viff.dev/viff/pkg/codec"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/entity/entitytest"
)

// A DEEP READ ADDRESS ANSWERS, whichever way it is spelled: a whole kinded path into the payload,
// quoted where a segment holds a dot, or `at:` the node and `path:` the field inside it. The
// quoted one used to fail as "nothing stands there".
func TestADeepReadAddressAnswers(t *testing.T) {
	store := entitytest.Store(t)
	setPayload(t, store, entity.RefAt("viff-dev", "drift", "head", "abc"), `{result: {report: hello}}`)
	deep := "head-moved@git.head.viff@aaaa1111"
	setPayload(t, store, entity.RefAt("viff-dev", "drift", "head", deep), `{result: {report: deep}}`)
	setPayload(t, store, entity.RefAt("viff-dev", "drift", "head", "v1.2.3"), `{result: {report: dotted}}`)
	snap := entity.Reader(store).(entity.Snapshot)

	for _, tc := range []struct {
		name string
		node func(t *testing.T) *ir.Node
		want string
	}{
		{"a kinded path", getPathOf("viff-dev.drift.head.abc.result.report"), "hello"},
		{"a quoted segment holding @", getPathOf(`viff-dev.drift.head."head-moved@git.head.viff@aaaa1111".result.report`), "deep"},
		{"a quoted segment holding dots", getPathOf(`viff-dev.drift.head."v1.2.3".result.report`), "dotted"},
		{"at and path", readNode(`!get-path {at: 'viff-dev.drift.head."v1.2.3"', path: result.report}`), "dotted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := readSpec(tc.node(t))
			if err != nil {
				t.Fatalf("readSpec: %v", err)
			}
			v, err := spec.get(snap)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			got, err := codec.Marshal(v, codec.Tony)
			if err != nil {
				t.Fatalf("emit: %v", err)
			}
			if string(got) != tc.want+"\n" {
				t.Fatalf("answered %q, want %q", got, tc.want)
			}
		})
	}
}

// getPathOf is a `!get-path` whose address is one kinded path string, built as a node so the quoting in
// it is the path's own rather than tony's.
func getPathOf(addr string) func(*testing.T) *ir.Node {
	return func(*testing.T) *ir.Node { return ir.FromString(addr).WithTag("!" + GetPathTag) }
}

// readNode is a read written as tony.
func readNode(src string) func(*testing.T) *ir.Node {
	return func(t *testing.T) *ir.Node {
		n, err := codec.ParseNode([]byte(src), codec.Tony)
		if err != nil {
			t.Fatalf("parse %s: %v", src, err)
		}
		return n
	}
}
