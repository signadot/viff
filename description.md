# An ask that settles without an answer reads as pending

An ask can settle with no answer: `status: orphaned` and a `why`, when verse cannot pose it (versed/asks.go, "it could not be asked: …") or an onward ask is withdrawn (versed/onwardfollow.go). mirror.AskRow shows `answer: pending` whenever the record has no answer, and carries neither its status nor its why, so `verse ask`, verse_ask and verse-ask-get all show a settled ask as waiting, and a task polling it waits on what nobody will answer. A record GET /asks could not read (`unreadable`) reads pending too.

Fix: the row shows the ask's `status`; `pending` only while it has not settled; the `why` of one that settled without an answer; and `unreadable` where the record does not read.