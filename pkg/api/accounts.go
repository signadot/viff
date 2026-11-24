package api

// WhoAmI is what `GET /whoami` answers: who the request is from, and how viffd knows it. By
// is the provider that verified them, or `asserted` for a caller who is whoever their request
// said; Via is the io that relayed them, where one did. A request that names nobody is answered
// with none of them.
//
//tony:schemagen=viff-whoami,notag
type WhoAmI struct {
	Principal string `tony:"field=principal,omitzero"`
	By        string `tony:"field=by,omitzero"`
	Via       string `tony:"field=via,omitzero"`
}

// Account is an account in an external system bound to a principal, as the account routes
// answer it: `GET /accounts` (the caller's own), `GET /accounts/{provider}/who`, and what a
// verify, a heard or a link answers once it binds. Claimant is how the claim was made; Via the io
// a person was relayed from.
//
//tony:schemagen=viff-account,notag
type Account struct {
	Provider  string        `tony:"field=provider"`
	Principal string        `tony:"field=principal,omitzero"`
	Iss       string        `tony:"field=iss,omitzero"`
	Sub       string        `tony:"field=sub,omitzero"`
	Login     string        `tony:"field=login,omitzero"`
	Claimant  string        `tony:"field=claimant,omitzero"`
	Via       string        `tony:"field=via,omitzero"`
	Proof     *AccountProof `tony:"field=proof,omitzero"`
}

// AccountProof is what a binding's proof kept, by its Method: a reflected comment's address
// (Evidence, At), a token's Iss, Sub and Iat, or the IO and Account a person was relayed from or
// heard by.
//
//tony:schemagen=viff-account-proof,notag
type AccountProof struct {
	Method   string `tony:"field=method,omitzero"`
	Evidence string `tony:"field=evidence,omitzero"`
	Iss      string `tony:"field=iss,omitzero"`
	Sub      string `tony:"field=sub,omitzero"`
	Iat      string `tony:"field=iat,omitzero"`
	IO       string `tony:"field=io,omitzero"`
	Account  string `tony:"field=account,omitzero"`
	At       string `tony:"field=at,omitzero"`
}

// AccountClaim is what `POST /accounts/{provider}` takes: for `github`, the login the caller
// says is theirs; for a provider whose io hears the nonce said, nothing.
//
//tony:schemagen=viff-account-claim,notag
type AccountClaim struct {
	Login string `tony:"field=login,omitzero"`
}

// AccountClaimed is what `POST /accounts/{provider}` answers: the nonce to say from the account,
// when the claim Expires, and the line to Say, word for word.
//
//tony:schemagen=viff-account-claimed,notag
type AccountClaimed struct {
	Nonce   string `tony:"field=nonce"`
	Expires string `tony:"field=expires"`
	Say     string `tony:"field=say"`
	Login   string `tony:"field=login,omitzero"`
}

// AccountVerify is what `POST /accounts/github/verify` takes: the nonce the account has said.
//
//tony:schemagen=viff-account-verify,notag
type AccountVerify struct {
	Nonce string `tony:"field=nonce"`
}

// AccountHeard is what `POST /accounts/{provider}/heard` takes, from a trusted io: a nonce it
// heard said, the account that said it, and the account's login where the system has one.
//
//tony:schemagen=viff-account-heard,notag
type AccountHeard struct {
	Nonce   string `tony:"field=nonce"`
	Account string `tony:"field=account"`
	Login   string `tony:"field=login,omitzero"`
}

// AccountLink is what `POST /accounts/{provider}/link` takes: an id_token from that provider, as
// evidence that the caller holds the account it names.
//
//tony:schemagen=viff-account-link,notag
type AccountLink struct {
	IDToken string `tony:"field=idToken"`
}
