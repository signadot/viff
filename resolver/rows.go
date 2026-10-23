package resolver

// Row is one row of `GET /resolvers`: a resolver as installed, and where.
//
//tony:schemagen=verse-resolver-row,notag
type Row struct {
	Resolver
	At string `tony:"field=at"`
	// Unreadable is why the stored registration does not decode as this verse reads it. Nothing
	// else on such a row is read from the record: the row is there so the registration can be
	// seen and removed by its name.
	Unreadable string `tony:"field=unreadable,omitzero"`
}

// Added is what `POST /resolvers` answers: where the resolver was installed.
//
//tony:schemagen=verse-resolver-added,notag
type Added struct {
	At string `tony:"field=at"`
}
