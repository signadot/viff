package resolver

// Row is a resolver as `GET /resolvers` lists it and `POST /resolvers` answers it: as
// installed, and where.
//
//tony:schemagen=viff-resolver-row,notag
type Row struct {
	Resolver
	At string `tony:"field=at"`
	// Unreadable is why the stored registration does not decode as this viff instance reads it. Nothing
	// else on such a row is read from the record: the row is there so the registration can be
	// seen and removed by its name.
	Unreadable string `tony:"field=unreadable,omitzero"`
}
