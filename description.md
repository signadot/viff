# Second review of the verse-system-base fixes: the doc example's then, failed spawns retiring a live entry, the tool's non-string inputs

A second review of 0957ddf43 and c3083ac18 found:

1. docs/systems/verse.md's `then` is refused at raise: its git block names no `from`, and the task's says `from: main` (`its from is unsaid, and the ceiling's is main`). Its script pushes `verse/ask`, a branch its checkout does not have. Nothing reads the example, so nothing caught it.
2. A spawn refused before its launch (runner, credential, launch check, principal, gate credential) reflects without hosting, so `reflect` reads the serial of whatever entry stands under the id, and the failed spawn's end retires a live run's entry an hour later. The serial should be the caller's, with none for a spawn that hosted nothing.
3. verse-ask-create's list and object branches (`to`, `label`, `then`, `else`) are documented as what verse-call-tool hands it, but every parameter is a string and calltool.Check refuses anything else; only a model off its schema reaches them, and an object of labels with a comma in a value is mis-split. The tool takes strings, as verse_ask_create does, and refuses anything else rather than reading it as nothing.
4. `hostedSerialOf`'s comment calls it `hostedSerial`, and `Meta.Environment`'s comment broke its reflow.