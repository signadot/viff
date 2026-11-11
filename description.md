# iod github: a listed pull request says merged: false

The github iod reads pull requests from GitHub's pulls listing (`listPulls`), which has `merged_at` and no `merged` field. `pull` decodes `merged` anyway, so `pullPayload` reflects `merged: false` for every pull request, merged or not. Seen on signadot/verse#1: GitHub says MERGED at 2026-11-06T12:55:11Z; the reflection says `state: closed, merged: false, mergedAt: "2026-11-06T12:55:11Z"`.

A charter that ends a record on `merged` (the software-factory's `end-pr`) ends a merged pull request as closed.

Fix: `merged` is `p.Merged || p.MergedAt != ""` in `pullPayload`, so it is true from either endpoint.