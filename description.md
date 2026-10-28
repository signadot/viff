# verse.ask-list: a task lists its owner's asks

A verse CLI user lists their asks with `verse ask`, and verse_ask with no name does the same; a task has verse-ask-get, which needs a name or an id, and cannot list. A task has the capabilities of a verse CLI user, so add the verse system's `ask-list` (tool `verse-ask-list`): the owner's asks as `verse ask` lists them, carried as GET /asks like ask-get.

Also: the runner's carrier (Guard.Carry) is tested only with a POST; a GET, with no body, no Content-Type and `format=tony` on the query, is not exercised. Test the request a GET carries.