package versed

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"

	"github.com/signadot/tony-format/go-tony/ir"

	"github.com/signadot/verse/entity"
	"github.com/signadot/verse/launch"
	"github.com/signadot/verse/systems"
	"github.com/signadot/verse/task"
	"github.com/signadot/verse/trigger"
)

// A CHARTER A TASK INSTALLS LAUNCHES NO MORE THAN THE TASK MAY, LESS INSTALLING CHARTERS. A
// runner carries a task's install (`verse-charter-install`) as the task's owner, and the
// charter stands after the task ends, its rules firing as that owner: unheld, a task given
// only `verse` would install a rule whose spawn reaches every system the owner can. So each
// spawn among its rules is held within the task (taskCharter), as the rows of an ask's `on`
// are (taskBound):
//
//   - the environment is the task's less `verse.charter-install`, so nothing a task installs,
//     and nothing that follows its asks, installs charters, and neither grows a chain of them;
//   - any runner, where a `then` runs on the task's: a rule fires long after the task, and
//     wherever its spawn says it runs.
//
// The autonomy is held to the task's as it stands at the install, and the credential is the
// task's, named as the task's spawn named it, where the spawn spends one: a script may name none
// (spendsWithin).
//
// AT THE INSTALL, AND AT A CHECK, every spawn as the charter writes it: an attribute still to be
// bound is within where some value of it could be (systems.Environment.WithinAsWritten). The rule
// records the task and what it could hand on (trigger.Trigger.Task, TaskEnvironment), and AT EACH
// FIRING the spawn, bound, is held to that (spawnerFor): a `$[repo]` the firing binds to a
// repository the task never reached does not launch. The rule keeps its own hold because it
// outlives the task's record: a scoped task's record goes with its scope, and its install went
// to baseline. Autonomy and credential are written into the spawn and never bound, so the
// install has them whole.
//
// A rule the task's owner installs over the task's is theirs, and held by nothing the task was.

// taskCharter is what a charter a task installs is held within: the task's launch less
// `verse.charter-install` (launched.handsOn), and the autonomy it runs under now.
type taskCharter struct {
	task        string
	environment systems.Environment
	autonomy    task.Autonomy
	virtualKey  string
}

// taskCharterOf is the hold on a charter the request installs or checks: nil for a request no
// runner carries for a task, and an error for a task verse holds no launch of.
func (s *Server) taskCharterOf(r *http.Request) (*taskCharter, error) {
	id := relayedTask(r)
	if id == "" {
		return nil, nil
	}
	return s.taskCharterFor(id)
}

// taskCharterFor is the hold on a charter task id installs, as verse hosts it now.
func (s *Server) taskCharterFor(id string) (*taskCharter, error) {
	ht := s.hostedOf(id)
	if ht.launched == nil {
		return nil, fmt.Errorf("task %s installs a charter, and verse holds no launch of it to hold "+
			"the charter's spawns within, so it is refused", id)
	}
	return &taskCharter{
		task:        id,
		environment: ht.launched.handsOn(),
		autonomy:    ht.autonomy,
		virtualKey:  ht.launched.virtualKey,
	}, nil
}

// handsOn is what a task may hand on, to what follows its asks and to what its charters spawn:
// the environment it was launched with, less installing charters, so neither grows a chain of
// them. Every hold within a task reads it here rather than the environment itself.
func (l *launched) handsOn() systems.Environment {
	system, verb, _ := systems.Split(task.StepVerseCharterInstall)
	return l.environment.Without(system, verb)
}

// spendsWithin is whether a spawn's credential is the task's: a spawn with a prompt spends the
// one it names, the runtime's default where it names none, so it names the task's as the task's
// spawn named it; a script spends none, so it may name none.
func spendsWithin(spec launch.Spec, key string) bool {
	return spec.VirtualKey == key || (spec.Script != "" && spec.VirtualKey == "")
}

// holds is why a rule would launch beyond the task installing it, or nil: each spawn's
// environment, as written, within the task's less charter-install, its autonomy no looser than
// the task's, and naming the task's credential. Nil for a request that is no task's.
func (h *taskCharter) holds(t trigger.Trigger) error {
	if h == nil {
		return nil
	}
	var err error
	t.Action.EachSpawn(func(chain string, spec *launch.Spec) {
		if err != nil {
			return
		}
		at := fmt.Sprintf("rule %q: action%s.spawn", t.Name, chain)
		if werr := spec.Environment.WithinAsWritten(h.environment); werr != nil {
			err = fmt.Errorf("%s.environment.%w — a charter task %s installs launches within the task, "+
				"less %s", at, werr, h.task, task.StepVerseCharterInstall)
			return
		}
		if aerr := spec.Autonomy.Within(h.autonomy, spec.Environment); aerr != nil {
			err = fmt.Errorf("%s.autonomy%w — a charter task %s installs launches under autonomy no "+
				"looser than the task's", at, aerr, h.task)
			return
		}
		if !spendsWithin(*spec, h.virtualKey) {
			err = fmt.Errorf("%s.virtualKey: %s, and task %s spends %s: a charter a task installs "+
				"spends the task's credential, named as the task's spawn named it", at,
				cmp.Or(spec.VirtualKey, "the runtime's default"), h.task,
				cmp.Or(h.virtualKey, "the runtime's default"))
		}
	})
	return err
}

// heldByItsTask is why a spawn of rule, bound by its firing, would reach beyond the task that
// installed the rule, or nil: within what the task could hand on when it installed it
// (Trigger.TaskEnvironment, launched.handsOn then), which the rule's record keeps. A rule no task
// installed is held by nothing here. The task's own record is not read: it may be gone, with the
// scope the task ran in, while the rule stands.
func (s *Server) heldByItsTask(rule trigger.Trigger, spec launch.Spec) error {
	if rule.Task == "" {
		return nil
	}
	name := rule.Name
	if rule.TaskEnvironment == nil {
		return errors.New("rule " + name + " was installed by task " + rule.Task + ", and its record " +
			"keeps nothing to hold the spawn within, so it is refused")
	}
	if err := spec.Environment.Within(*rule.TaskEnvironment); err != nil {
		return fmt.Errorf("environment.%w — rule %s was installed by task %s, and launches within "+
			"the task, less %s", err, name, rule.Task, task.StepVerseCharterInstall)
	}
	return nil
}

// markTaskRules marks each listed rule a task installed with `task: <id>`, which a charter file
// says as a note (charterfile): the task is the record's, never the rule's text.
func markTaskRules(listed []trigger.Trigger, rules *ir.Node) {
	if rules == nil || rules.Type != ir.ArrayType {
		return
	}
	by := map[string]string{}
	for _, t := range listed {
		if t.Task != "" {
			by[t.Name] = t.Task
		}
	}
	for _, rule := range rules.Values {
		id, ok := by[entity.Str(rule, "name")]
		if !ok || rule.Type != ir.ObjectType {
			continue
		}
		rule.Fields = append(rule.Fields, ir.FromString("task"))
		rule.Values = append(rule.Values, ir.FromString(id))
	}
}
