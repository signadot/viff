# proposal (deferred): a GitHub App io

**Deferred.** This is not on the path of the repo-charters work (jdvbwvja), which extends the existing token-based github io instead. Pick it up only when something proves a GitHub App necessary.

What an App would give over the existing io's fine-grained personal access token:
- an identity of its own, not tied to a person;
- check runs, including action buttons that reach the app as webhooks;
- a required status check pinned to the app as its source, so nobody else can satisfy it;
- the app itself in a ruleset's bypass list;
- higher rate limits at org scale (the existing io's comments already say an org-scale install wants an App; see 3eskdzrq).

Why it is a project of its own, not a flag on the existing io:
- **Its configuration is not ours to control.** Permissions, subscribed events and the webhook URL live in GitHub's settings and are changed there. The app manifest flow creates an app from a manifest once, but there is no ongoing config-as-code, and the vendor changes that surface on its own schedule.
- **Registration, a private key and installations per org** are deployment work that the token path does not need.

Related: jdvbwvja (the review environment), 3eskdzrq (residual: the GitHub source).