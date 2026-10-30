# Routing: a spawn goes only to runners its spawner may use

Follow-up from the #59 review (issuecomment-6526296183, point 1); g6fnhmrs step 3.

`candidates` (versed/route.go) considers every described registration, and nothing relates whoever registered a runner to whoever spawns. The seed holds `owner` beneath `verse.runners` out to everyone (only staging narrows it, in repopulate.sh), and registering needs only a subject the person holds, which a static token gives.

So on an open verse, a runner that offers `verse` and scripts with wide ceilings, and reports each task done at once so its load stays 0, receives every spawn it fits:
- the prompt, the repos, the resolved virtual key and a minted credential;
- with the record naming its subject, so `relayTask` admits it as the task's owner.

On main before #59, a runner got work only from a charter that named it.

Options discussed:
- route only among runners the spawner (or the charter's owner) may use: a grant on the registration;
- keep the open default, but route only to registrations the system or admin wrote.

Staging is mostly covered meanwhile: repopulate.sh lets admin alone register.