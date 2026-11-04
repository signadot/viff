package trigger

// A SUSPENSION is one record naming a charter (or every charter), a rule of it (or all), and a
// subject (or every subject), and it covers the work about that subject in every form: a rule
// starts no firing about it, a task about it holds, a question about it is withdrawn, a hook's
// launch is parked. docs/control.md describes it, and docs/control.md#suspending says what it
// does to rules; this file is the record and the table an engine asks.
//
// The table is one per scope and written by one writer — the route that also writes the store,
// in the same order — so it is never fed by a watch that could race that writer.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/mirror"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger/charter"
)

// SuspensionKind is where the records live: verse.trigger.suspension.<id>.
const SuspensionKind = "suspension"

// Every is the wildcard a suspension spells for "every charter", "every rule" and "every
// subject", and a subject's wildcard for one segment.
const Every = "*"

// Recursive is how a suspension's id spells a charter named with everything beneath it.
const Recursive = ".**"

// SuspensionRef names the record of one suspension.
func SuspensionRef(id string) entity.Ref {
	return entity.VerseRef(charter.System, SuspensionKind, id)
}

// IsSuspension reports whether an address is a suspension's record.
func IsSuspension(r entity.Ref) bool {
	return r.ChildOf(entity.VerseRoot, charter.System, SuspensionKind)
}

// Suspension is one record: what it covers, the scope it was made in, and who holds it.
type Suspension struct {
	// Charter is an installed charter's address, "" for baseline's, or Every for every charter.
	Charter string
	// Recursive names the charter at Charter and every charter beneath it.
	Recursive bool
	// Rule is a rule's name in the charter, or Every.
	Rule string
	// Subject is the path the suspension holds work about, as a rule's subject is a path: a
	// segment of Every reaches any name there. `*` is the path every address is under.
	Subject string
	// Scope is the scope the suspension was made in, "" for baseline, as a rule record's.
	Scope string
	// Held is who holds it, by principal. A suspension stands while anybody does.
	Held map[string]Holder

	charter    []string // parsed
	charterAll bool     // Every
	subject    []string // parsed steps, Every where a segment is open
	subjectM   Match    // the selector the steps read as
}

// Holder is one party's hold on a suspension: why, since when, and the store revision the hold
// was written at, which is what the listing measures movement under the cover from.
type Holder struct {
	Why        string
	Since      string
	Occurrence int64
}

// NewSuspension reads the parts of a suspension into their one spelling, so two spellings of one
// address are one record: addresses are parsed and rendered by segment, never compared as
// strings, since a string comparison would let a respelling fail open.
//
// The record spells every part: every charter and every subject are Every, an empty charter is
// baseline's, and an empty subject is refused. A flag left out on the command line is filled in
// before it gets here (versed's selectionOf), since an empty charter already means baseline's.
func NewSuspension(charter, rule, subject string, recursive bool) (Suspension, error) {
	s := Suspension{Recursive: recursive}
	switch charter {
	case Every:
		if recursive {
			return Suspension{}, fmt.Errorf("charter %q: every charter has nothing beneath it to add", charter)
		}
		s.Charter, s.charterAll = Every, true
	case "":
		if recursive {
			return Suspension{}, fmt.Errorf("baseline's charter and everything beneath it is every "+
				"charter, which is spelled %q", Every)
		}
	default:
		if strings.Contains(charter, Every) {
			return Suspension{}, fmt.Errorf("charter %q: a charter is named by its address, and "+
				"everything beneath one by recursive — a wildcard inside an address names no place "+
				"whose owner could stand behind it", charter)
		}
		st, err := ParseAt(charter)
		if err != nil {
			return Suspension{}, fmt.Errorf("charter %q: %w", charter, err)
		}
		s.charter = st.Path()
		s.Charter = strings.Join(s.charter, ".")
	}
	switch rule {
	case "", Every:
		s.Rule = Every
	default:
		if err := validTriggerName(rule); err != nil {
			return Suspension{}, fmt.Errorf("rule: %w", err)
		}
		s.Rule = rule
	}
	steps, m, err := subjectSelector(subject)
	if err != nil {
		return Suspension{}, err
	}
	s.subject, s.subjectM = steps, m
	s.Subject = joinSelector(steps)
	return s, nil
}

// subjectSelector reads a suspension's subject as the flow selector it is (ParseExpr), and the
// steps its one spelling is rendered from. A trailing `*` asks for the depth, and stays. A payload
// predicate has no string spelling, and would let an ordinary write take the subject out of the
// match and so lift the suspension.
func subjectSelector(subject string) ([]string, Match, error) {
	if strings.TrimSpace(subject) == "" {
		return nil, nil, fmt.Errorf("a suspension names its subject: an address, or %q for every subject", Every)
	}
	m, err := ParseExpr(subject)
	if err != nil {
		return nil, nil, fmt.Errorf("subject %q: %w", subject, err)
	}
	if m.HasPredicate() {
		return nil, nil, fmt.Errorf("subject %q: a suspension's subject has no payload predicate", subject)
	}
	// NAMES AND `*`, because the table renders its subject back as steps and a match over a
	// segment has no step spelling. The one way to write one today is a quoted `"*"` or `".."`,
	// which names a node whose name is those characters.
	for _, seg := range m.SegNodes() {
		if _, ok := entity.PathLiteral(seg); ok || isAnySeg(seg) {
			continue
		}
		return nil, nil, fmt.Errorf("subject %q: a suspension's subject is names and `*`, and a "+
			"name that would read as one of those has no spelling here", subject)
	}
	steps, err := entity.ExprSegs(strings.TrimSpace(subject))
	if err != nil {
		return nil, nil, fmt.Errorf("subject %q: %w", subject, err)
	}
	return steps, m, nil
}

// joinSelector renders a selector's steps, an open segment bare and every other quoted as an
// address needs it.
func joinSelector(steps []string) string {
	parts := make([]string, len(steps))
	for i, st := range steps {
		if st == Every {
			parts[i] = Every
			continue
		}
		parts[i] = entity.JoinSegs([]string{st})
	}
	return strings.Join(parts, ".")
}

// ID is the record's id: `<charter>:<rule>@<subject>`, the spelling a task id shares
// (ownerOf), baseline's charter being the empty one and a recursive charter `<address>.**`.
func (s Suspension) ID() string {
	charter := s.Charter
	if s.Recursive {
		charter += Recursive
	}
	return charter + AtSep + s.Rule + ChainSep + s.Subject
}

// Payload is the record as it is written, holders included.
func (s Suspension) Payload() entity.Payload {
	p := entity.Obj{"charter": s.Charter, "rule": s.Rule, "subject": s.Subject}
	if s.Recursive {
		p["recursive"] = true
	}
	if s.Scope != "" {
		p[charter.ScopeField] = s.Scope
	}
	held := entity.Obj{}
	for who, h := range s.Held {
		held[who] = h.payload()
	}
	p["held"] = held
	return p.Payload()
}

func (h Holder) payload() entity.Payload {
	o := entity.Obj{"why": h.Why, "since": h.Since}
	if h.Occurrence != 0 {
		o["occurrence"] = h.Occurrence
	}
	return o.Payload()
}

// HolderPayload is one holder as the record carries it under `held.<principal>`.
func HolderPayload(h Holder) entity.Payload { return h.payload() }

// SuspensionOf reads a record back.
func SuspensionOf(p entity.Payload) (Suspension, error) {
	s, err := NewSuspension(entity.Str(p, "charter"), entity.Str(p, "rule"), entity.Str(p, "subject"),
		recursiveOf(p))
	if err != nil {
		return Suspension{}, err
	}
	s.Scope = entity.Str(p, charter.ScopeField)
	s.Held = map[string]Holder{}
	for who, h := range entity.Fields(entity.Get(p, "held")) {
		s.Held[who] = Holder{Why: entity.Str(h, "why"), Since: entity.Str(h, "since"),
			Occurrence: mirror.OccurrenceOf(h)}
	}
	return s, nil
}

// recursiveOf reads a record's `recursive`, false unless it is true.
func recursiveOf(p entity.Payload) bool {
	n := ir.Uncomment(entity.Get(p, "recursive"))
	return n != nil && n.Type == ir.BoolType && n.Bool
}

// Holders is who holds it, sorted.
func (s Suspension) Holders() []string {
	out := make([]string, 0, len(s.Held))
	for who := range s.Held {
		out = append(out, who)
	}
	sort.Strings(out)
	return out
}

// CoversCharter reports whether the suspension names the charter at this address (none for
// baseline's) and the rule by this name in it. A rule of "" — work no rule started, a task run
// by hand — is named only by a suspension of every rule of every charter.
func (s Suspension) CoversCharter(at []string, rule string) bool {
	if rule == "" {
		return s.charterAll && s.Rule == Every
	}
	if s.Rule != Every && s.Rule != rule {
		return false
	}
	switch {
	case s.charterAll:
		return true
	case s.Recursive:
		return entity.Under(s.charter, at, entity.Literal)
	default:
		return slices.Equal(s.charter, at)
	}
}

// CoversSubject reports whether the suspension names this subject, by the selector a flow is
// listed by. Work about no subject is named only by a suspension of every subject.
func (s Suspension) CoversSubject(subj entity.Ref) bool {
	// The subject is a PATH, asked as a rule's subject is: what its path reaches. Work about no
	// subject stands under no path, so no subject names it.
	if subj == (entity.Ref{}) {
		return false
	}
	return s.subjectM.MatchRef(subj)
}

// EverySubject reports whether its subject path reaches every subject — what a whole rule being
// held means. It is a question about the path, answered by containment: whether it contains the
// path of one `*`, which every address is under.
func (s Suspension) EverySubject() bool { return matchContains(s.subjectM, everySubject) }

// everySubject is the path every address is under: one segment, any name.
var everySubject = Match{mKeyPath: []*ir.Node{ir.FromString(Every)}}

// EveryCharter reports whether it names every charter.
func (s Suspension) EveryCharter() bool { return s.charterAll }

// CharterPlace is the node control over the charter side is asked at, when the suspension names
// one: a package's address, or a baseline rule's own record. A suspension of every charter, or of
// every rule of baseline's, names none.
func (s Suspension) CharterPlace() (entity.Ref, bool) {
	switch {
	case s.charterAll:
		return entity.Ref{}, false
	case len(s.charter) > 0:
		return entity.RefAt(s.charter...), true
	case s.Rule != Every:
		return charter.RuleRef("", s.Rule), true
	}
	return entity.Ref{}, false
}

// SubjectPlace is the node control over the subject side is asked at when the suspension is
// written: the selector's literal head, the segments before its first open one. A selector open
// from its first segment names none. What it covers is asked again at each concrete subject.
func (s Suspension) SubjectPlace() (entity.Ref, bool) {
	var head []string
	for _, st := range s.subject {
		if strings.Contains(st, Every) {
			break
		}
		head = append(head, st)
	}
	if len(head) == 0 {
		return entity.Ref{}, false
	}
	return entity.RefAt(head...), true
}

// Within reports whether everything s covers, o covers too: what a lift says when the pair it
// lifted is still held by something wider. It answers by the spellings, so a glob inside a
// segment covers only the same glob; false is "not shown to", never "not covered".
func (s Suspension) Within(o Suspension) bool {
	switch {
	case o.charterAll:
	case s.charterAll:
		return false
	case o.Recursive:
		if !entity.Under(o.charter, s.charter, entity.Literal) {
			return false
		}
	default:
		if s.Recursive || !slices.Equal(o.charter, s.charter) {
			return false
		}
	}
	if o.Rule != Every && o.Rule != s.Rule {
		return false
	}
	// Everything s's subject path reaches, o's reaches too: path containment, as a stratum asks it.
	return matchContains(o.subjectM, s.subjectM)
}

// CharterFlag is the charter side as `--charter` spells it: the address, and `:<rule>` when it
// names one rule. Baseline's charter is the empty address, so all its rules are `:`.
func (s Suspension) CharterFlag() string {
	switch {
	case s.Rule != Every:
		return s.Charter + AtSep + s.Rule
	case s.Charter == "":
		return AtSep
	}
	return s.Charter
}

// ParseCharterFlag splits `--charter <address>[:<rule>]` on its last colon, since a rule's name
// holds none: `verse-dev.drift:claim`, `:claim` for baseline's rule, `:` for all of baseline's
// rules, and an address alone for every rule of the charter there.
func ParseCharterFlag(s string) (charter, rule string) {
	if i := strings.LastIndex(s, AtSep); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, Every
}

// Suspensions is one scope's table: the suspensions in force, held in memory and written only by
// the route that writes their records. Until it has loaded it answers suspended for everything,
// since a table that failed open on a restart would resume what somebody stopped on purpose.
type Suspensions struct {
	mu      sync.RWMutex
	loaded  bool
	entries map[string]Suspension
	// draining is the suspensions being lifted: still refusing new firings while what they held
	// is released (Lift).
	draining map[string]bool
	engines  []*Engine
}

// NewSuspensions is an empty table that has not loaded.
func NewSuspensions() *Suspensions {
	return &Suspensions{entries: map[string]Suspension{}, draining: map[string]bool{}}
}

// SetSuspensions gives the engine its scope's table, which it asks wherever work would make
// progress. An engine with none suspends nothing.
func (e *Engine) SetSuspensions(t *Suspensions) {
	e.susp = t
	if t == nil {
		return
	}
	t.mu.Lock()
	t.engines = append(t.engines, e)
	t.mu.Unlock()
}

// Detach drops an engine from the table, when the engine stops.
func (t *Suspensions) Detach(e *Engine) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, x := range t.engines {
		if x == e {
			t.engines = append(t.engines[:i], t.engines[i+1:]...)
			return
		}
	}
}

// Load fills the table from the records its scope's store holds, and marks it loaded.
func (t *Suspensions) Load(records []Suspension) {
	t.mu.Lock()
	for _, s := range records {
		t.entries[s.ID()] = s
	}
	t.loaded = true
	engines := t.attached()
	t.mu.Unlock()
	// What the table held while it had not loaded, it releases now: every rule it skipped is
	// armed again, since nothing recorded which.
	for _, e := range engines {
		e.releaseTasks()
		e.releaseLaunches()
		e.signalLift()
		e.reArm(func(string) bool { return true })
	}
}

// attached is the engines asking this table, copied under its lock so they are told of a change
// after it is released.
func (t *Suspensions) attached() []*Engine {
	return append([]*Engine(nil), t.engines...)
}

// Unload forgets the table's suspensions and holds everything until the next Load: what a restore
// does first, since the records it is about to read are the only account of what is suspended.
func (t *Suspensions) Unload() {
	t.mu.Lock()
	t.entries = map[string]Suspension{}
	t.loaded = false
	t.mu.Unlock()
}

// Loaded reports whether the table has loaded.
func (t *Suspensions) Loaded() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.loaded
}

// Get is one suspension by id.
func (t *Suspensions) Get(id string) (Suspension, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.entries[id]
	return s, ok
}

// List is every suspension in force, by id.
func (t *Suspensions) List() []Suspension {
	t.mu.RLock()
	out := make([]Suspension, 0, len(t.entries))
	for _, s := range t.entries {
		out = append(out, s)
	}
	t.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// Put records a suspension — new, or with its holders changed — once its record is written, and
// has every engine asking the table hold what it now covers.
func (t *Suspensions) Put(s Suspension) {
	t.mu.Lock()
	t.entries[s.ID()] = s
	engines := t.attached()
	t.mu.Unlock()
	for _, e := range engines {
		e.withdrawHeld()
		e.holdTasks()
	}
}

// Lift releases a suspension once its record is deleted, IN ORDER, since releasing everything at
// once would let a rule see its subject in the state a hook left it in, with the task that hook
// was about to start not yet live, and start one of its own:
//
//  1. the entry is marked draining, so new firings about its work are still refused;
//  2. what it held is released — tasks it paused resume, parked launches start in order,
//     withdrawn questions are asked again with the time they had left;
//  3. the entry is removed;
//  4. the rules it covered are armed again, so each acts on the world as it stands now, the path
//     an install and a restart take.
//
// A wide lift is a restart's worth of evaluation, at a moment an operator chose.
func (t *Suspensions) Lift(id string) {
	t.mu.Lock()
	s, ok := t.entries[id]
	if !ok {
		t.mu.Unlock()
		return
	}
	t.draining[id] = true
	engines := t.attached()
	t.mu.Unlock()
	for _, e := range engines {
		e.releaseTasks()
		e.releaseLaunches()
		e.signalLift()
	}
	t.mu.Lock()
	delete(t.entries, id)
	delete(t.draining, id)
	t.mu.Unlock()
	for _, e := range engines {
		at := e.atSegs()
		e.reArm(func(rule string) bool { return s.CoversCharter(at, rule) })
	}
}

// notLoaded is what holds everything while a table has not loaded, named as a suspension would be.
const notLoaded = "the suspensions have not loaded"

// covering is the suspensions naming (at, rule, subject) — or, for whole, naming the rule about
// every subject — and whether the table has loaded; settled leaves out the ones being lifted.
// Copied out under the read lock, so a caller asking who may hold them never holds the table
// while it asks.
func (t *Suspensions) covering(at []string, rule string, subj entity.Ref, whole, settled bool) (bool, []Suspension) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.loaded {
		return false, nil
	}
	var out []Suspension
	for id, s := range t.entries {
		if settled && t.draining[id] {
			continue
		}
		if !s.CoversCharter(at, rule) {
			continue
		}
		if whole && !s.EverySubject() {
			continue
		}
		if !whole && !s.CoversSubject(subj) {
			continue
		}
		out = append(out, s)
	}
	return true, out
}

// holding is the ids of the suspensions holding the work a rule does about a subject in this
// engine now — rule "" for work no rule started, a zero subject for work about none. Empty is
// "not suspended". Asked wherever work would make progress; never in subject selection, which is
// what decides a question has gone moot, so a suspension there would moot work rather than hold it.
//
// A suspension being lifted still counts here: until the lift has released what it held, a new
// firing about its work would run beside the launch a hook parked rather than after it.
func (e *Engine) holding(rule string, subj entity.Ref) []string {
	return e.held(rule, subj, false, false)
}

// holdingRule is holding for the rule about every subject: what the top of evaluate asks, before
// firstSight records the commit, since no subject has been selected there to ask about.
func (e *Engine) holdingRule(rule string) []string {
	return e.held(rule, entity.Ref{}, true, false)
}

// settled is holding without the suspensions being lifted: what the paths a lift releases ask —
// a paused task resuming, a parked launch starting, a withdrawn question asked again — so they
// go ahead while new firings are still refused.
func (e *Engine) settled(rule string, subj entity.Ref) []string {
	return e.held(rule, subj, false, true)
}

func (e *Engine) held(rule string, subj entity.Ref, whole, settled bool) []string {
	if e.susp == nil {
		return nil
	}
	loaded, cands := e.susp.covering(e.atSegs(), rule, subj, whole, settled)
	if !loaded {
		return []string{notLoaded}
	}
	var out []string
	for _, s := range cands {
		for _, who := range s.Holders() {
			if e.mayHold(who, rule, subj) {
				out = append(out, s.ID())
				break
			}
		}
	}
	sort.Strings(out) // one order, so a task's holders compare and its record does not churn
	return out
}

// mayHold reports whether a holder's suspension covers this work, asked NOW rather than when the
// suspension was written, so a wildcard's reach does not grow as owners appear beneath it: from
// the charter side, control over the charter; from the subject side, control at the subject. A
// rule the system installed is the system's to suspend, from either side. A check the mirror
// cannot answer covers, as a perms check that cannot be answered refuses: the work stays held.
func (e *Engine) mayHold(holder, rule string, subj entity.Ref) bool {
	if entity.IsSystem(holder) {
		return true
	}
	if rule != "" && entity.IsSystem(e.OwnerOf(rule)) {
		return false
	}
	if entity.PolicyOf(e.store) == nil {
		return true // a store that attributes nothing has nobody to refuse
	}
	holds := func(ref entity.Ref) bool {
		ok, err := entity.Holds(e.store, holder, entity.Control, ref)
		return err != nil || ok
	}
	if rule != "" && holds(e.charterPlace(rule)) {
		return true
	}
	return subj != (entity.Ref{}) && holds(subj)
}

// charterPlace is the node control over a rule's charter is held at: a package's address, or a
// baseline or scope rule's own record.
func (e *Engine) charterPlace(rule string) entity.Ref {
	if e.confine.confined() {
		return entity.RefAt(e.confine.Path()...)
	}
	return charter.RuleRef("", rule)
}

// atSegs is this engine's charter address as segments, none for an unaddressed charter.
func (e *Engine) atSegs() []string {
	if !e.confine.confined() {
		return nil
	}
	return e.confine.Path()
}

// recordsCompletion reports whether a chain is a completion hook's or a sequence step's: work
// that records what a task did, whose writes land under a suspension while its launches park.
func recordsCompletion(chain string) bool {
	segs := strings.FieldsFunc(chain, func(r rune) bool { return r == '.' || r == '[' || r == ']' })
	for _, seg := range segs[min(1, len(segs)):] { // past the rule's own name
		switch seg {
		case "onDone", "onFailed", "onCancelled", "onOrphaned", seqField:
			return true
		}
	}
	return false
}

// SuspendedError is the cause a question is withdrawn with: the suspensions holding the work it
// is about. The docket renders it as the question's `why`.
type SuspendedError struct{ IDs []string }

// Suspends says this withdrawal holds the work to ask it again (task.Suspends).
func (SuspendedError) Suspends() bool { return true }

func (err SuspendedError) Error() string {
	return "withdrawn while " + strings.Join(err.IDs, ", ") + " suspends the work it is about; " +
		"it is asked again when that lifts"
}

// ask poses a firing's gate question and waits for its answer, for as long as the work it is
// about is not suspended. Each pose has a context of its own under the mootness context, so a
// suspension withdraws the question — the record goes orphaned with the suspension as its `why`
// and keeps its ballots and its deadline stamp — while the firing keeps its flight and waits for
// the lift. Then, if the rule still selects the subject, it asks the same question again under
// the same id, joining what was cast, with the time it had left on its clock. Mootness still ends
// a suspended firing, since the pose sits under it.
func (e *Engine) ask(wctx context.Context, fl *flight, t Trigger, subj entity.Ref, d task.GateQuestion) (task.Resolution, error) {
	tool := task.ToolUse{Name: "verse:trigger:" + t.Name}
	for withdrawn := false; ; {
		if !e.awaitLift(wctx, t.Name, subj) {
			return task.Resolution{By: task.ByCancelled, Question: d}, context.Cause(wctx)
		}
		if withdrawn {
			if !e.stillSelects(t, subj) {
				err := fmt.Errorf("%s no longer selects %s, so the question it asked is moot", t.Name, subj)
				return task.Resolution{By: task.ByCancelled, Question: d}, err
			}
			e.resumeClock(d.ID)
		}
		pctx, cancel := context.WithCancelCause(wctx)
		done := fl.posing(cancel)
		// Asked again once the pose is registered: a suspension landing between the wait above and
		// here has already walked the flights, and would otherwise be missed.
		if held := e.settled(t.Name, subj); len(held) > 0 {
			cancel(SuspendedError{held})
		}
		res, err := e.resolver.Resolve(pctx, d, tool)
		done()
		var susp SuspendedError
		if err != nil && wctx.Err() == nil && errors.As(context.Cause(pctx), &susp) {
			cancel(nil)
			e.tracef("gate %s on %s: %v", d.ID, subj, susp)
			fl.setWithdrawn(susp.IDs)
			withdrawn = true
			continue
		}
		fl.setWithdrawn(nil)
		cancel(nil)
		return res, err
	}
}

// awaitLift waits until the work is not suspended, or ctx ends; false means it ended.
func (e *Engine) awaitLift(ctx context.Context, rule string, subj entity.Ref) bool {
	for {
		lifted := e.liftSignal() // before the question, so a lift in between still wakes it
		if len(e.settled(rule, subj)) == 0 {
			return true
		}
		select {
		case <-lifted:
		case <-ctx.Done():
			return false
		}
	}
}

// liftSignal is closed at the next lift.
func (e *Engine) liftSignal() <-chan struct{} {
	e.liftMu.Lock()
	defer e.liftMu.Unlock()
	if e.lifted == nil {
		e.lifted = make(chan struct{})
	}
	return e.lifted
}

// signalLift wakes everything waiting on a lift.
func (e *Engine) signalLift() {
	e.liftMu.Lock()
	defer e.liftMu.Unlock()
	if e.lifted != nil {
		close(e.lifted)
	}
	e.lifted = make(chan struct{})
}

// withdrawHeld withdraws every question a firing of this engine is asking about work now held.
func (e *Engine) withdrawHeld() {
	for _, f := range e.flights() {
		if held := e.settled(f.rule, f.subj); len(held) > 0 {
			f.withdraw(SuspendedError{held})
		}
	}
}

// resumeClock gives a withdrawn question the time it had left, from now.
func (e *Engine) resumeClock(id string) {
	if err := mirror.ResumeDeadline(e.store, id, time.Now()); err != nil {
		e.logger.Warn("a withdrawn question's deadline could not be moved to its lift",
			"question", id, "err", err)
	}
}

// subjectOf is a live task's subject as an address; zero for a task about none.
func subjectOf(s string) entity.Ref {
	ref, _ := entity.ParseAddr(s)
	return ref
}

// setHold changes a live task's hold under the registry lock and hands back the task as it now
// stands; false when the task is no longer live.
func (e *Engine) setHold(id string, fn func(*flowTask)) (flowTask, bool) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	fp, ok := e.live[id]
	if !ok {
		return flowTask{}, false
	}
	fn(&fp)
	e.live[id] = fp
	return fp, true
}

// recordHold writes who holds a task onto its record, and removes it when nothing does. Only onto
// a record that exists: a task this engine adopted with no record gets none invented for it.
func (e *Engine) recordHold(fp flowTask) {
	ref := mirror.Ref(fp.id)
	var v entity.Payload
	if by := fp.heldBy(); len(by) > 0 {
		v = entity.Val(by)
	}
	_, _, _ = e.store.Write(entity.Write{
		At:    ref.At(mirror.HeldByField),
		Value: v,
		When:  []entity.Require{{At: ref.At(), Pattern: entity.Present()}},
	})
}

// holdTasks has every live task say which suspensions hold it now, and holds at its next boundary
// the ones nothing held before. A task already held — by the operator, or by another suspension —
// is not paused again, but a suspension that also covers it is named on it, so its record and a
// listing count what each suspension holds.
func (e *Engine) holdTasks() {
	for _, fp := range e.liveTasks() {
		held := e.settled(mirror.TriggerOf(fp.chain), subjectOf(fp.subject))
		if len(held) == 0 || slices.Equal(held, fp.suspended) {
			continue
		}
		var was bool
		fp, ok := e.setHold(fp.id, func(fp *flowTask) {
			was = fp.operator || len(fp.suspended) > 0
			fp.suspended = held
		})
		if !ok {
			continue
		}
		if !was {
			fp.h.Pause(engineActor)
		}
		e.recordHold(fp)
	}
}

// releaseTasks resumes every live task a suspension held that none holds now, unless an operator
// holds it too: a lift resumes only what the suspension paused.
func (e *Engine) releaseTasks() {
	for _, fp := range e.liveTasks() {
		if len(fp.suspended) == 0 {
			continue
		}
		held := e.settled(mirror.TriggerOf(fp.chain), subjectOf(fp.subject))
		fp, ok := e.setHold(fp.id, func(fp *flowTask) { fp.suspended = held })
		if !ok || len(held) > 0 {
			continue
		}
		if !fp.operator {
			fp.h.Resume(engineActor)
		}
		e.recordHold(fp)
	}
}

// liveTasks is every live task, copied out of the registry.
func (e *Engine) liveTasks() []flowTask {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	out := make([]flowTask, 0, len(e.live))
	for _, fp := range e.live {
		out = append(out, fp)
	}
	return out
}

// parkedLaunch is launchSpawn's arguments, held until the work they are about is released.
type parkedLaunch struct {
	a          Action
	chain      string
	subj       entity.Ref
	binding    launch.Binding
	occurrence int64
	passed     []launch.Passed
}

// park holds a launch.
func (e *Engine) park(l parkedLaunch) {
	e.parkMu.Lock()
	e.parked = append(e.parked, l)
	e.parkMu.Unlock()
}

// parkedFor reports whether a launch of chain about subj is parked: a hooked spawn's claim
// stays with it (applyOne), since the lift starts the task, and the task's end releases it.
func (e *Engine) parkedFor(chain string, subj entity.Ref) bool {
	e.parkMu.Lock()
	defer e.parkMu.Unlock()
	for _, l := range e.parked {
		if l.chain == chain && l.subj == subj {
			return true
		}
	}
	return false
}

// Parked is how many launches this engine is holding.
func (e *Engine) Parked() int {
	e.parkMu.Lock()
	defer e.parkMu.Unlock()
	return len(e.parked)
}

// releaseLaunches starts, in the order they were parked, every launch nothing holds now, and keeps
// the rest.
func (e *Engine) releaseLaunches() {
	e.parkMu.Lock()
	all := e.parked
	e.parked = nil
	e.parkMu.Unlock()
	var kept []parkedLaunch
	for _, l := range all {
		if len(e.settled(mirror.TriggerOf(l.chain), l.subj)) > 0 {
			kept = append(kept, l)
			continue
		}
		h, err := e.launch(l.a, l.chain, l.subj, l.binding, l.occurrence, l.passed)
		if err != nil {
			e.logger.Warn("a parked launch did not start", "chain", l.chain, "subject", l.subj.String(), "err", err)
		}
		// The claim a hooked spawn took rode with the parked launch (applyOne); a launch that
		// started nothing leaves no task to release it.
		if l.a.hooked() && h == nil {
			e.releaseRun(l.chain, l.subj)
		}
	}
	if len(kept) > 0 {
		e.parkMu.Lock()
		e.parked = append(kept, e.parked...)
		e.parkMu.Unlock()
	}
}

// reArm arms again every installed rule covers names, so each replays the world as it stands:
// a subtree watch opened again replays its opening state as a commit, and a root-loop engine
// evaluates the rule at the head.
//
// What the rule has been evaluated at from the head on is first forgotten. A skip before
// firstSight records nothing for a rule suspended whole, but a rule suspended about some subjects
// goes on evaluating the others, and a sibling's commit landing while a lift drains records the
// head the replay would arrive at — refusing the one evaluation that sees the subject it skipped.
func (e *Engine) reArm(covers func(rule string) bool) {
	e.trigMu.Lock()
	trigs := make([]Trigger, 0, len(e.triggers))
	for _, t := range e.triggers {
		if covers(t.Name) {
			trigs = append(trigs, t)
		}
	}
	e.trigMu.Unlock()
	if len(trigs) == 0 || e.bgctx == nil {
		return // not observing yet: arming the rule is what starting does
	}
	head := e.store.Rev()
	for _, t := range trigs {
		e.seenMu.Lock()
		if s := e.seen[t.Name]; s != nil {
			s.forgetFrom(head)
		}
		e.seenMu.Unlock()
		if e.subtree {
			// Back to pending, its watches stopped, for the reconciler to open again: the
			// opening state they replay is the world as it stands (reconcile.go).
			e.disarm(t.Name, "armed again after a lift")
			continue
		}
		go e.evaluate(e.bgctx, t, entity.Commit{Rev: head})
	}
	if e.subtree {
		e.kick()
	}
}

// disarm takes an armed rule back to pending with its watches stopped, for the reconciler to
// arm again; a rule not armed is left as it is.
func (e *Engine) disarm(name, why string) {
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	for i, t := range e.triggers {
		if t.Name != name {
			continue
		}
		e.triggers = slices.Delete(slices.Clone(e.triggers), i, i+1)
		e.stopWatchers(name)
		e.hold(t, cause{causePass, why})
		return
	}
}

// HeldNow is everything this engine holds under suspensions now, each piece once however many
// suspensions cover it: the live tasks a suspension holds, and how many questions are withdrawn
// and launches parked.
func (e *Engine) HeldNow() (tasks []LiveTask, questions, launches int) {
	for _, fp := range e.liveTasks() {
		if len(fp.suspended) > 0 {
			tasks = append(tasks, fp.live())
		}
	}
	for _, f := range e.flights() {
		f.mu.Lock()
		if len(f.withdrawn) > 0 {
			questions++
		}
		f.mu.Unlock()
	}
	return tasks, questions, e.Parked()
}

// Holds is what this engine holds for one suspension now: live tasks it paused, questions it has
// withdrawn, and launches parked that it covers.
func (e *Engine) Holds(id string) (tasks, questions, launches int) {
	for _, fp := range e.liveTasks() {
		if slices.Contains(fp.suspended, id) {
			tasks++
		}
	}
	for _, f := range e.flights() {
		f.mu.Lock()
		if slices.Contains(f.withdrawn, id) {
			questions++
		}
		f.mu.Unlock()
	}
	e.parkMu.Lock()
	parked := append([]parkedLaunch(nil), e.parked...)
	e.parkMu.Unlock()
	for _, l := range parked {
		if slices.Contains(e.settled(mirror.TriggerOf(l.chain), l.subj), id) {
			launches++
		}
	}
	return tasks, questions, launches
}

// Names reports whether a suspension names anything this engine runs: an installed rule of its
// charter, or — for every rule of every charter — the work no rule started too. A suspension that
// names nothing anywhere holds nothing, and its listing says so; a package moved or a rule renamed
// leaves one behind.
func (e *Engine) Names(s Suspension) bool {
	if s.EveryCharter() && s.Rule == Every {
		return true
	}
	at := e.atSegs()
	e.trigMu.Lock()
	defer e.trigMu.Unlock()
	for _, set := range [][]Trigger{e.triggers, e.pendingTriggers()} {
		for _, t := range set {
			if s.CoversCharter(at, t.Name) {
				return true
			}
		}
	}
	return false
}

// SuspendedWhole is what holds a rule about every subject, when something does: what the charter
// listing marks a rule with.
func (e *Engine) SuspendedWhole(rule string) []string {
	return e.holdingRule(rule)
}
