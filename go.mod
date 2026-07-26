module github.com/signadot/verse

go 1.25.1

require (
	github.com/fatih/color v1.19.0
	github.com/scott-cotton/cli v0.2.3
	github.com/signadot/tony-format/go-tony v0.0.94
)

require (
	github.com/evanphx/json-patch v0.5.2 // indirect
	github.com/expr-lang/expr v1.17.8 // indirect
	github.com/go-air/gini v1.0.4 // indirect
	github.com/google/gops v0.3.28 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	golang.org/x/mod v0.30.0 // indirect
	golang.org/x/sync v0.18.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/tools v0.39.0 // indirect
)

tool (
	github.com/signadot/tony-format/go-tony/cmd/o
	github.com/signadot/tony-format/go-tony/cmd/tony-codegen
)
