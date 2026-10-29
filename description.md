# versed: GET /charter reads each package's declaration twice

`fillCharter` (versed/charter.go) calls `packageSends(at)` and then `charterExpiry(at)`; each reads the package declaration with `packageDecl`. A tree over N packages makes 2N declaration reads where N would do. On staging, reads over the index walk are the logd load.

Read the declaration once in fillCharter and take the sends and the ttl/expires off it.