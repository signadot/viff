# iod git: an annotated tag's trailers on its ref

A deploy tag on verse-staging names the verse commit it deploys in its annotation (`commit: 0cc21d35`), but `git.<repo>.ref.<name>` carries only `repo, name, sha, type, tree`, so verse-staging's deploy-announce charter cannot link the deploy's tree and history on signadot/verse: it links verse-staging's, which is the staging config, not what was deployed.

The annotation is already a trailer block by git's reading and by `parseTrailers`: top-level `key: value` lines, nested ones indented as continuations.

Change: the iod reflects an annotated tag's trailers on its ref as `trailers`, the same shape and the same parser as a commit's; absent when the tag has none, and never on a branch or a lightweight tag (a branch's commit already carries its trailers at `git.<repo>.commit.<sha>`).