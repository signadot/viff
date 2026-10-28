package trigger

import (
	"sort"
	"strings"
	"time"

	"github.com/signadot/verse/entity"
)

// WHAT WENT WRONG AT A RULE'S FIRINGS, kept for the rule's author.
//
// A rule that did nothing looks the same from outside whether it selected nothing, was denied,
// or tried and failed, and only the last is something its author can fix. The engine knows
// which it was at the moment it happens, and the daemon's log is read by whoever runs the
// daemon. So it is kept here, and shown where the author looks: on the rule in the charter's
// listing, and as a count in the status.
//
// THE LATEST OF EACH KIND PER RULE, with how many times it has happened. One line answers
// "is this rule in trouble, and with what"; a history of every occurrence is the log's.
//
// IN MEMORY, as `fired` is: it is about this engine since it started. A rule taken out of the
// engine takes its trouble with it, so a rule reinstalled with its mistake corrected reads
// clean.

// The kinds of trouble.
const (
	// TroubleFailed is an action that did not do what the rule says: a reference that did not
	// resolve, a patch that would land on a value, a derive that failed, a spawn no doer is
	// eligible for or whose doer cannot be launched under it, a gate's pose that did not expand.
	//
	// A SPAWN THAT STARTED AND FAILED IS NOT HERE. A runner that is not installed, or a driver
	// the runtime does not have, is a task that failed: its record says so, and the rule's
	// `onFailed` hook fires. What is kept here is what leaves no task to say it.
	TroubleFailed = "failed"
	// TroubleUnread is a commit the rule could not read, and so did not evaluate. The commit
	// is marked seen before it is read (firstSight), so the rule does not look at it again.
	TroubleUnread = "unread"
)

// Trouble is one kind of trouble a rule is in.
type Trouble struct {
	Rule string `tony:"field=rule"`
	Kind string `tony:"field=kind"`
	// Chain is the place in the rule that failed — `review`, `review.onDone`,
	// `review.approval.onDenied` — and absent for a commit that was not read, which is the
	// whole rule's.
	Chain string `tony:"field=chain,omitzero"`
	// Subject is what the firing was about, where there was one.
	Subject string `tony:"field=subject,omitzero"`
	// Rev is the commit the rule could not read.
	Rev int64  `tony:"field=rev,omitzero"`
	Why string `tony:"field=why"`
	// At is when it last happened (RFC3339), and Count how many times since the rule was
	// installed or the engine started.
	At    string `tony:"field=at"`
	Count int    `tony:"field=count"`
}

// ChainRule is the rule a chain belongs to: the chain's first segment. A rule's name holds no
// dot (validTriggerName), so the first one bounds it. One reading, for whoever asks which rule
// a firing is of: who it acts as (Principal), what it may write (reservedWrite), its trouble, and
// the daemon's hold on a task's rule.
func ChainRule(chain string) string {
	rule, _, _ := strings.Cut(chainID(chain), ".")
	return rule
}

// failed records that an action at chain, about subj, did not do what the rule says.
func (e *Engine) failed(chain string, subj entity.Ref, err error) {
	if err == nil {
		return
	}
	t := Trouble{Rule: ChainRule(chain), Kind: TroubleFailed, Chain: chainID(chain), Why: err.Error()}
	if len(subj.Segs()) > 0 {
		t.Subject = subj.String()
	}
	e.trouble(t)
}

// unread records that rule could not read the verse at rev, and did not evaluate the commit.
func (e *Engine) unread(rule string, rev int64, err error) {
	e.trouble(Trouble{Rule: rule, Kind: TroubleUnread, Rev: rev, Why: err.Error()})
}

func (e *Engine) trouble(t Trouble) {
	t.At = time.Now().UTC().Format(time.RFC3339)
	key := t.Rule + "\x00" + t.Kind
	e.mu.Lock()
	if e.troubles == nil {
		e.troubles = map[string]Trouble{}
	}
	t.Count = e.troubles[key].Count + 1
	e.troubles[key] = t
	e.mu.Unlock()
	if e.logger != nil {
		e.logger.Warn("a rule's firing went wrong", "rule", t.Rule, "kind", t.Kind,
			"chain", t.Chain, "subject", t.Subject, "rev", t.Rev, "why", t.Why, "count", t.Count)
	}
}

// forgetTrouble drops what is kept about a rule that is leaving the engine.
func (e *Engine) forgetTrouble(rule string) {
	e.mu.Lock()
	for _, kind := range []string{TroubleFailed, TroubleUnread} {
		delete(e.troubles, rule+"\x00"+kind)
	}
	e.mu.Unlock()
}

// Troubles is what has gone wrong at this engine's rules, by rule and then by kind.
func (e *Engine) Troubles() []Trouble {
	e.mu.Lock()
	out := make([]Trouble, 0, len(e.troubles))
	for _, t := range e.troubles {
		out = append(out, t)
	}
	e.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// TroubleCounts is how many firings have failed, and how many commits went unread, across this
// engine's rules as they stand.
func (e *Engine) TroubleCounts() (failed, unread int) {
	for _, t := range e.Troubles() {
		switch t.Kind {
		case TroubleFailed:
			failed += t.Count
		case TroubleUnread:
			unread += t.Count
		}
	}
	return failed, unread
}
