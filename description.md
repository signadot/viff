# secret.md: the samples admit NODE_OPTIONS, and nothing says the command's writes are unbounded

From Daniel's review of PR #6 (pullrequestreview-5859518014), two things in `docs/systems/secret.md`:

1. The environment example and the operator's sample ceiling both admit `NODE_OPTIONS: null`. Any value includes `--require ./steal.js`, which runs workspace code inside npm with `NPM_TOKEN` set. Operators copy the sample, so it should not carry it. The samples keep `NPM_CONFIG_REGISTRY` with its pattern as the variable a shape names.

2. The document says the network bounds where the secret can go, and says nothing of writes. The command runs over the task's workspace, so what it runs can leave the value in a file the task reads afterwards, which neither the network nor the redaction covers. A paragraph beside the network's says so, and "in no log, no reply and no file" under *Where the secret lives* is scoped to the runner, since it read as a promise about the command too.

Docs only. Bounding writes is not attempted here: nothing bounds a task's writes anywhere, which is the larger question.