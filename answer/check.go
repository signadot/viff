package answer

import (
	"errors"
	"fmt"
)

// Checker adapts a Contract to what the task loop consults (task.AnswerCheck): a predicate on
// the value a run is about to return, asked at the moment the input goes quiet.
//
// It lives here rather than in each driver because it was in each driver — llmDriver and the
// coderunner's commandDriver carried the same twenty lines against different back ends, with
// the repair budget duplicated. That was a consequence of WHERE the check ran: inside `Next`,
// because a rejection has to re-enter the conversation and the driver was the only thing
// holding one. The loop has that mechanism too and uses it for people, so the check moved to
// where the whole pending list is visible and the drivers stopped needing a copy.
//
// Why the loop calling it is not the same as the driver calling it: the driver checked when the
// MODEL stopped talking, which with a child still editing the tree is not when the ANSWER is
// final. The loop asks when the input has actually gone quiet, and asks again if it reopens.
type Checker struct {
	C *Contract
	// FailHint is what the repair tells the model to do instead of inventing a value it does
	// not have — the name of the fail tool, for a driver that has one, or a phrase for one that
	// does not. It matters: a repair loop pressures a model toward compliance, and naming the
	// honest exit is what keeps a contract a guarantee about shape rather than an invitation.
	FailHint string

	tries int
}

// Check reports the repair to feed back, or an error that fails the run, or neither.
func (k *Checker) Check(answer string) (string, error) {
	if k == nil || k.C == nil {
		return "", nil
	}
	_, err := k.C.Check(answer)
	var f *Failure
	if err == nil {
		return "", nil
	} else if !errors.As(err, &f) {
		// Not something the model can fix — the contract itself is unusable. Failing on the
		// spot is right: retrying would spend the budget on the spawn's own bug.
		return "", err
	} else if k.tries >= k.C.Repairs() {
		// Bounded on purpose. If the model cannot produce the shape with the specific
		// complaint in front of it, more turns will not teach it, and an unconvincible model
		// failing loudly is the honest end.
		return "", fmt.Errorf("answer contract not satisfied after %d repair attempt(s): %w", k.tries, err)
	} else {
		k.tries++
		return f.Repair(k.C, k.FailHint), nil
	}
}
