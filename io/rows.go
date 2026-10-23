package io

// Row is one row of `GET /iods`: the registration, and what verse knows of the io now.
// LastPass is when the live reflector last completed a pass, which catches a source that is
// alive and reflecting nothing new; Running is false only for a registration with no reflector
// running. Reach and Why are there only with `?probe=true`.
//
//tony:schemagen=verse-iod-row,notag
type Row struct {
	Entity
	At       string `tony:"field=at"`
	LastPass string `tony:"field=lastPass,omitzero"`
	Running  *bool  `tony:"field=running,omitzero"`
	Reach    string `tony:"field=reach,omitzero"`
	Why      string `tony:"field=why,omitzero"`
	// Unreadable is why the stored registration does not decode as this verse reads it. Nothing
	// else on such a row is read from the record: the row is there so the registration can be
	// seen and removed by its name.
	Unreadable string `tony:"field=unreadable,omitzero"`
}

// Added is what `POST /iods` answers: where the io was installed, what the probe found unless
// `?probe=false`, and what it claims to own as it said so. An io installed but not reachable,
// or not running, says why in Warning.
//
//tony:schemagen=verse-iod-added,notag
type Added struct {
	At      string `tony:"field=at"`
	Reach   string `tony:"field=reach,omitzero"`
	Warning string `tony:"field=warning,omitzero"`
	Owns    []Own  `tony:"field=owns,omitzero"`
}

// Checked is what `GET /iods/{name}/check` answers: the io asked what it is, now. Says is what
// it claims now, beside what the registration recorded at install; neither is merged into the
// other, since a claim that has drifted is two values an operator should see.
//
//tony:schemagen=verse-iod-check,notag
type Checked struct {
	At    string `tony:"field=at"`
	Reach string `tony:"field=reach"`
	URL   string `tony:"field=url,omitzero"`
	Why   string `tony:"field=why,omitzero"`
	Says  *Says  `tony:"field=says,omitzero"`
}

// Says is what an io says of itself: the root it writes and the places it claims.
//
//tony:schemagen=verse-iod-says,notag
type Says struct {
	Root string `tony:"field=root"`
	Owns []Own  `tony:"field=owns,omitzero"`
}
