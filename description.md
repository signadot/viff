# verse data --depth counts segments from the root, where it reads as depth below the path given

`--depth <n>` bounds the walk to addresses of at most n segments counted from the ROOT
of the verse, not n levels below the address asked for. The reference says this
accurately ("bounds the walk to addresses of at most <n> segments", docs/reference/README.md)
but every other flag on the command is relative to the path you named, so it reads as
relative and fails silently when it is not.

What it looks like from the outside — same data, three answers:

    $ verse data git-issue issue --paths --depth 1
    (nothing)
    $ verse data git-issue issue --paths --depth 2
    (nothing)
    $ verse data git-issue --paths --depth 3
    git-issue issue
    git-issue issue tony-format/0dxprv4ch12kr4n0fxn0
    ...

The first two are the natural spellings for "list what is directly under this", and both
answer with nothing, exit 0. `git-issue.issue.<repo>/<id>` is three segments, so anything
under `git-issue issue` needs `--depth 3` — a number about a path the reader did not type.
The deeper the address, the further the useful number drifts from the small one they reach
for, and an empty listing is a legitimate answer everywhere else in the CLI, so nothing
says they asked the wrong question.

Two directions, and I do not think it is obvious which:

- Make it relative to the address given, so `--depth 1` under `git-issue issue` lists its
  children. Matches how the rest of the command reads. Changes the meaning of an existing
  flag, so it is a contract break.
- Keep it absolute and make it say so when the bound is already shallower than the path
  it was handed — `--depth 1` under a 2-segment address can only ever answer with nothing,
  and that is knowable before the walk. A refusal naming the number that would work costs
  nothing and is not a break.

The second is cheap and catches the whole failure mode, which is somebody reading an empty
listing as an empty verse. Found rehearsing the lab walkthrough, on the git-issue tree.