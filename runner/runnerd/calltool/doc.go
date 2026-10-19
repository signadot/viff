// Package calltool is how a PROGRAM in a task's workspace calls the task's tools: `question-ask`,
// `git-push`, `git-fetch`, `github-create-pr`, `github-update-pr`, and whatever the runner
// offers next. The verse agent has those tools because each is wired to the loop in Go; a script
// has only a shell. This gives the shell one command, `verse-call-tool` (cmd/verse-call-tool), and
// the task process one socket to answer it on.
//
// It knows no agent package: the host offers its tools as Tool and performs a call by name.
//
// The two ends speak HTTP over a unix socket, with tony bodies through package codec, and there
// are two routes:
//
//	GET  /tools   the tools on offer, each as the model sees it: name, description, params, required
//	POST /call    one call — {tool, input} — answered with what it came to: {out, refused}
//
// The binary knows no tool by name. It reads the list, turns a tool's params into flags, and
// sends the call; a tool added on the server is a tool the shell has, with its flags, the next
// time it runs. That is what lets the list grow without the binary changing.
//
// What PERFORMING a call means is the host's to say, through Server.Call. In a task process the
// host is the script's driver: the call goes in front of the loop as an ordinary action, as the
// agent's use of the same tool would, the task's policy decides it, the docket lists it,
// the read plane narrates it, and the tool's own Do runs once it is approved. A call the gate
// declines comes back refused — an answer the caller re-plans on, as against a failure.
//
// The socket is a PATH, where the delivery channel is an inherited descriptor, because the whole
// point is that the shell reaches it. What keeps that from being a way past the gate is that the
// server does not perform anything itself: it hands the call to the host, and the host gates it.
// The path reaches a program as the variable Env names.
package calltool
