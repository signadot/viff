package versed

import (
	"fmt"
	"sort"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/api"
	"github.com/signadot/verse/codec"
	"github.com/signadot/verse/entity"
)

// runnersSystem is where installed runners live: `verse.runners.<name>`.
//
// A runner is an ENTITY rather than a startup flag, because the ask this answers is
// "deploying arbitrary runners in the verse namespace" — adding one is a deployment act, a
// write, not an edit to how verse itself is started. A drive adds a runner the way it adds a
// doer or a resolver.
const runnersSystem = "runners"

// runnerEntityRef addresses an installed runner: one name beneath the registry, which is the whole of its
// address.
func runnerEntityRef(name string) entity.Ref {
	return entity.VerseRef(runnersSystem, name)
}

// checkRunnerFields reports a field the decoder would drop in silence.
//
// Read as a DOCUMENT before decoding, for the reason a resolver is: gomap ignores what it does
// not recognise, so `ulr:` would install a runner with no address and report success — and the
// url is the entire content of this entity, so the silence is total.
func checkRunnerFields(node *ir.Node) error {
	known := map[string]string{"name": "", "url": "", "identity": ""}
	node = ir.Uncomment(node)
	if node == nil || node.Type != ir.ObjectType {
		return nil // not an object: the decoder's complaint to make, in its own terms
	}
	for _, field := range node.Fields {
		name := field.String
		if _, ok := known[name]; ok {
			continue
		}
		want := make([]string, 0, len(known))
		for k := range known {
			want = append(want, k)
		}
		sort.Strings(want)
		if name == "described" || name == "offers" {
			return fmt.Errorf("what a runner offers is read from the runner, which describes itself " +
				"when it is installed and checked; it is not written by hand")
		}
		return fmt.Errorf("runner has no field %q (want any of %s) — an unknown field decodes "+
			"silently, and url is the whole of what this entity carries", name, strings.Join(want, ", "))
	}
	return nil
}

// findRunner reads one installed runner by what a charter or a CLI argument names: its address as it
// is stored and listed (`verse.runners.gpu`), or its name (`gpu`) — an operator reading `verse
// data` and one typing `verse runner show` should not have to know two spellings of one thing.
//
// findRunnerErr is the same read reporting whether it HAPPENED, and it is the one a caller deciding
// something uses: a read that failed, or one the reader may not make, is not a runner that is
// not installed, and a spawn told "not installed" is work that does not run.
func findRunner(r entity.Reader, ref string) (api.Runner, bool) {
	e, ok, _ := findRunnerErr(r, ref)
	return e, ok
}

func findRunnerErr(r entity.Reader, ref string) (api.Runner, bool, error) {
	name, ok := runnerNameOf(ref)
	if !ok {
		return api.Runner{}, false, nil
	}
	e, ok, err := r.Get(runnerEntityRef(name))
	if err != nil || !ok {
		return api.Runner{}, false, err
	}
	var out api.Runner
	if codec.FromNode(e.Payload, &out) != nil {
		return api.Runner{}, false, nil
	}
	return out, true, nil
}

// installedRunners reads every installed runner, ordered by address, so two readers see one order.
func installedRunners(r entity.Reader) []api.Runner {
	out, _ := installedRunnersErr(r)
	return out
}

// installedRunnersErr is installedRunners, reporting the error THIS read failed with — see entity.ListErr for why a
// listing cannot say so in its signature, and why asking the store afterwards is a different
// question from asking the read.
//
// AT THE SUBTREE, not over the document (readRegistrations). A List(pred) is a Match on the ROOT
// — the whole verse across the wire and filtered here — so a listing of a handful of runners cost
// a read of everything every source had ever reflected. A registration that does not decode is
// left out: what reads this decides with the runners it can reach.
func installedRunnersErr(r entity.Reader) ([]api.Runner, error) {
	out, _, err := readRegistrations[api.Runner](r, runnersSystem, isRunnerRef)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// isRunnerRef reports whether an address is an installed runner's — the namespace's own question, since
// the reserved root rather than the system is what makes it verse's (see mirror.IsTask).
func isRunnerRef(ref entity.Ref) bool { return ref.Under(entity.VerseRoot, runnersSystem) }

// runnerNameOf is the runner's name from either spelling an operator has in front of them: the whole
// address as it is listed and stored (`verse.runners.gpu`), or the name alone. Anything else —
// the address without its reserved root, a path with more to it — names no runner.
func runnerNameOf(ref string) (string, bool) {
	segs, err := entity.PathSegs(ref)
	if err != nil {
		return "", false
	}
	if at := entity.RefAt(segs...); at.ChildOf(entity.VerseRoot, runnersSystem) {
		return at.Name(), at.Name() != ""
	}
	if len(segs) != 1 || segs[0] == "" {
		return "", false
	}
	return segs[0], true
}
