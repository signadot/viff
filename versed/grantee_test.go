package versed

import (
	"testing"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/resolver"
	"github.com/signadot/verse/runner"
	"github.com/signadot/verse/source"
	"github.com/signadot/verse/trigger"
)

// The places the grammar says act are the places that do: a rule, a source and a binding each
// spell a principal the grammar reads, and a runner's address — which has a record and acts as
// its identity instead — is refused. entity names the three systems because it cannot import
// the packages that own them; this is where the two spellings are held together.
func TestTheGrammarsActorsAreTheOnesThatAct(t *testing.T) {
	for _, ref := range []entity.Ref{
		trigger.CharterRef("x"),
		trigger.CharterRefAt("drift", "head-moved"),
		source.Ref("git", "cluster"),
		resolver.Ref("security", "gate"),
	} {
		if _, err := entity.ParsePrincipal(entity.Via(ref)); err != nil {
			t.Errorf("%s acts, and its principal does not parse: %v", ref, err)
		}
	}
	if _, err := entity.ParseGrantee(entity.Via(runner.Ref("pool", "coderunner"))); err == nil {
		t.Errorf("a runner's address parsed as a place that acts")
	}
}
