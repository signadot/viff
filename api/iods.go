package api

// Outbox is one row of `GET /outboxes?to=<place>`: a package's declaration that it sends From
// its own address To an io's out place. Asked by the io that reads the place.
//
//tony:schemagen=verse-outbox,notag
type Outbox struct {
	From string `tony:"field=from"`
	To   string `tony:"field=to"`
}
