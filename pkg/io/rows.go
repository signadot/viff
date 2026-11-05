package io

// Row is an io as `GET /iods` lists it and `POST /iods` answers it: the registration, and what
// verse knows of the io now. LastPass is when the live reflector last completed a pass, which
// catches a source that is alive and reflecting nothing new; Running is false only for a
// registration with no reflector running. Reach and Why are what a probe found: on a listing only
// with `?probe=true`, on an install unless `?probe=false`. Warning is what the operator should
// act on: an install's, such as an io installed but not reachable or not running, or a listing's,
// a pass report it could not read. Report is what the out half last said of a pass, for a
// registration with an out half that has reported.
//
//tony:schemagen=verse-iod-row,notag
type Row struct {
	Entity
	At       string  `tony:"field=at"`
	LastPass string  `tony:"field=lastPass,omitzero"`
	Running  *bool   `tony:"field=running,omitzero"`
	Reach    string  `tony:"field=reach,omitzero"`
	Why      string  `tony:"field=why,omitzero"`
	Warning  string  `tony:"field=warning,omitzero"`
	Report   *Report `tony:"field=report,omitzero"`
	// Unreadable is why the stored registration does not decode as this verse reads it. Nothing
	// else on such a row is read from the record: the row is there so the registration can be
	// seen and removed by its name.
	Unreadable string `tony:"field=unreadable,omitzero"`
}

// Report is what an out half's last pass did, as it reported it (OutPass), and when a pass last
// finished without a failure. It is about now, so it stands in the store's scratch at the
// registration's `out` and never on the record: a pass costs no commit, and a `verse iod` listing
// reads it onto the row. The failure that stopped a pass is the record's (OutHalf.LastError).
//
//tony:schemagen=verse-iod-report,notag
type Report struct {
	LastPass  string `tony:"field=lastPass,omitzero"`
	Delivered int    `tony:"field=delivered,omitzero"`
	Failed    int    `tony:"field=failed,omitzero"`
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
