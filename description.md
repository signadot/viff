# A gate's mode is auto or ask: `park` and `hold` go

A rule's gate accepts `mode: park`, and `hold` as another spelling of it (`trigger.ParseMode`), but it has no behaviour of its own. The engine treats every mode but `auto` as a question it poses and waits on (`Mode.Blocks`, fireOne), so a parked gate is an ask gate. The gates reference documents `auto|ask` only. The name survives in the mode type and its comment, in `ParseMode` and its error, in a few comments, in the glossary's "mode" entry, and in docs/control.md's table of stops.

Remove `ModePark` and the `park` and `hold` spellings. A gate writing either is refused at install as an unknown mode, naming `auto|ask`. The comments and docs say a gate's mode is one of two.

No charter in verse, verse-charters, verse-workshop or on-verse writes either mode.