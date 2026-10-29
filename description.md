# versed: charterTx.expiry answers (503, nil) for a declaration it does not find

In versed/charterttl.go, `expiry` clears a ttl a fielded install no longer gives by reading the package declaration: `if err != nil || !had { return http.StatusServiceUnavailable, err }`. With `!had` and no error that is a 503 beside a nil error, which the caller (`err != nil`) reads as success. The behaviour happens to be right, since there is nothing to clear, but the code says the opposite, and the error branch has no `ttl:` context where its siblings carry one.

A declaration not found is nothing to take away: return Created. An error reading it says what was being read.