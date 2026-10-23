# versed: the system's unchanged rule is skipped only after its perms record is read

The skip from nf6fwh8g (e1bb05a4) leaves an unchanged system rule standing at a restart, but `tx.rule` decides it after `entity.SnapshotRecord`, which reads the rule's perms record from the store to undo a failed install. On staging that read, one per rule, bound "installed the system charters" at 1m53s on the e1bb05a4 start: the goroutine dump sat in it (`charter.go:1080`).

Whose a rule stands as is answered in memory (`entity.ActsAs`, the perms mirror), so the skip is decided first and the record is read only for an install that happens. Test: TestAnUnchangedSystemCharterIsNotReplayedAtARestart counts the start's reads of the rule's record, 0 with the change and 1 without.