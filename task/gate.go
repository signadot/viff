package task

import "slices"

// PrincipalAnswerer, in a gate's `answerers`, is whom the task acts for: the person or agent who
// ran it as themselves, or the asker a task acts for beside its rule. It is put as them when the
// question is posed. A gate naming nobody is the docket's, so a question for the task's own
// person says so: the `question-ask` tool and a conversation's turn name it.
const PrincipalAnswerer = "$principal"

// NamesPrincipal reports whether answerers name PrincipalAnswerer.
func NamesPrincipal(answerers []string) bool { return slices.Contains(answerers, PrincipalAnswerer) }
