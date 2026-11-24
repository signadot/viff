package api

import "go.viff.dev/viff/pkg/entity"

// Group is a named set of principals (`viff group add`): as a grantee, `group:<name>` admits its
// members. A member is a grantee in the grammar (entity.Grammar): a principal, `<kind>:*`, `*`,
// `group:<name>`, or a place acting.
//
//tony:schemagen=viff-group,notag
type Group struct {
	Name    string   `tony:"field=name"`
	Members []string `tony:"field=members,omitzero"`
}

// GroupRow is a group where it is stored, `mirror.principal.group.<name>`: one row of
// `GET /groups`, and what `GET /groups/{name}` and `POST /groups` answer.
//
//tony:schemagen=viff-group-row,notag
type GroupRow struct {
	Group
	At string `tony:"field=at"`
}

// PermsRecord is a node's perms record as its owner reads it back (`viff perms <path>`): who
// owns the node, what they hold out on it, and what a node created beneath it holds out.
//
//tony:schemagen=viff-perms-record,notag
type PermsRecord struct {
	At          string         `tony:"field=at"`
	Owner       string         `tony:"field=owner,omitzero"`
	Grants      entity.Payload `tony:"field=grants,omitzero"`
	ChildGrants entity.Payload `tony:"field=child-grants,omitzero"`
}

//tony:schemagen=viff-perms-edit,notag
type PermsEdit struct {
	At          string         `tony:"field=at"`
	ChildGrants entity.Payload `tony:"field=child-grants,omitzero"`
	Grants      entity.Payload `tony:"field=grants,omitzero"`
	Grant       entity.Payload `tony:"field=grant,omitzero"`
	Below       bool           `tony:"field=below,omitzero"`
	Revoke      entity.Payload `tony:"field=revoke,omitzero"`
}

// Perms is the permission check's census: refusals by the level a write or read wanted, checks
// the mirror could not answer, what listings left out, and grantee names the grammar does not
// read.
//
//tony:schemagen=viff-perms,notag
type Perms struct {
	Refused    map[string]int `tony:"field=refused,omitzero"`
	Unreadable int            `tony:"field=unreadable,omitzero"`
	// Hidden is what listings, replays and watches left out for a reader who may not see it —
	// apart from refused, since a listing that leaves something out is answered.
	Hidden int `tony:"field=hidden,omitzero"`
	// Unparsed is how many times a check met a grantee name the grammar does not read
	// (entity.Grammar), which admitted nobody. A stored grant from before the grammar is one;
	// zero is the expected reading.
	Unparsed int `tony:"field=unparsed,omitzero"`
}
