# Restore takes a failed read of the package declarations for "reads nothing" and refuses every confined package's rules

## What happened (staging, 2026-10-30)

At about 16:26:50Z the spot node running versed and three iods was interrupted. versed restarted, and its restore pushed docd over its memory limit: docd was OOMKilled at 16:27:25 (the docd headroom is a separate issue).

While the store was failing, versed restored its charter packages. Every confined package's rules were refused with "this package reads nothing". The `reads` records were there all along: `verse data verse trigger package` shows them now. Consequences:
- the `repo-charters.*` reconcilers came back as fresh renders;
- GitHub #37 ("verse/live is not what runs") reopened at 16:30;
- the reconciler asked on #59 at 16:34 (CHANGES_REQUESTED, `verse` status pending).

## Why

`restorePackageImports` (versed/packages.go) reads the declarations through `packageImports`, which has two silent reads:
- `entity.Below` over `verse.trigger.package`, which swallows a read error and answers no records;
- `packagesSeen`, which drops the error that `packagesSeenErr` reports.

So when the store fails, every package's declaration reads as empty, and `restorePackageImports` skips `SetReads` for it.

The rules read a few lines later uses `entity.BelowErr`, and it fails the restore when it can't read ("the charter could not be read, so no package was restored"). In this run that read succeeded, so the rules loaded against engines with no declaration. `checkReads` then refused every rule that reads anything. An empty answer and no answer are opposite facts here. `packagesSeenErr`'s own comment says so, and the restore gets that right for the rules and wrong for their declarations.

## Fix

- `packageImports` reads with `BelowErr` and `packagesSeenErr`, and returns the error.
- `restorePackageImports` returns it, and the restore fails with `entity.Unread` before any rule loads, the same way it does when the rules can't be read. Whatever retries a failed restore then retries this one too. If nothing does, that is part of this issue: a restore that can't read the store should wait for it, not install half a charter.
- Test: a store that fails the `verse.trigger.package` read restores no package and refuses no rule. Check it fails with the fix disabled.

The other `entity.Below` over `isCharterRecord` in `removePackage` (packages.go:614) has the same shape: a failed read there reads as "nothing left". Worth the same change while in there.