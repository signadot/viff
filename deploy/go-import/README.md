# go.viff.dev

What `go.viff.dev` serves: the page that tells the go command where the module
`go.viff.dev/viff` lives. It is a static site with no build step.

- `viff/index.html` carries the `go-import` tag naming the repository.
- `_redirects` (Netlify's) answers every path under `/viff/` with that page, since the go
  command asks at the path of whichever package it is resolving.

To publish: a Netlify site whose base directory is this one, with no build command and
`go.viff.dev` as a custom domain. The base directory is what keeps the repository's root
`netlify.toml`, the docs site's, from applying here. Then a CNAME `go` at the domain's DNS
pointing at the site's `<name>.netlify.app`.

To check:

```sh
curl -s 'https://go.viff.dev/viff/pkg/entity?go-get=1' | grep go-import
```

A second module on the host is a second directory with its own page and its own line in
`_redirects`.
