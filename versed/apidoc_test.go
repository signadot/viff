package versed

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE OPERATOR API'S DOC IS THE ROUTE TABLE. api/doc.go lists every route with the type it takes
// and the type it answers, and a reader trusts it as they trust runner/doc.go: so every route
// versed registers is a row there, every row is a route, and where a registration names a body
// the row names the same Go type, in the file that declares it.
func TestTheAPIDocIsTheRouteTable(t *testing.T) {
	types := schemaTypes(t, "../api", "../entity", "../trigger", "../task", "../runner", "../io", "../resolver", "../launch", "../systems")
	rows := docRows(t, "../api/doc.go")

	srv := testServer(t)
	srv.Handler()
	seen := map[string]bool{}
	for _, rt := range srv.routes {
		key := rt.Method + " " + rt.Path
		seen[key] = true
		row, ok := rows[key]
		if !ok {
			t.Errorf("%s is served and api/doc.go does not list it", key)
			continue
		}
		files := map[string]bool{}
		want := "-"
		if rt.Request != "" {
			ty := types[rt.Request]
			want = ty.name
			if ty.file != "" {
				files[ty.file] = true
			}
		}
		if row.request != want {
			t.Errorf("%s: api/doc.go says it takes %q; the registration says %q", key, row.request, want)
		}
		if rt.Response != "" {
			ty := types[rt.Response]
			want := map[string]string{"one": ty.name, "list": "[]" + ty.name, "stream": "SSE stream of " + ty.name}[rt.Returns]
			if row.response != want {
				t.Errorf("%s: api/doc.go says it answers %q; the registration says %q", key, row.response, want)
			}
			if ty.file != "" {
				files[ty.file] = true
			}
		}
		switch {
		case row.file != "" && !files[row.file]:
			t.Errorf("%s: api/doc.go names %s, which declares neither its request nor its answer", key, row.file)
		case row.file == "" && len(files) > 0:
			t.Errorf("%s: api/doc.go names no file, and its types are declared in package api", key)
		}
	}
	for key := range rows {
		if !seen[key] {
			t.Errorf("api/doc.go lists %s, which versed does not serve", key)
		}
	}
}

// docRow is one route as api/doc.go's table writes it.
type docRow struct{ request, response, file string }

// docRows reads the route table out of a package comment: each line that opens with a method,
// its columns apart by two spaces or more, the path read without its query.
func docRows(t *testing.T, path string) map[string]docRow {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`^//\t(GET|POST|PUT|PATCH|DELETE)\s+(\S+)\s{2,}(.*)$`)
	cols := regexp.MustCompile(`\s{2,}`)
	rows := map[string]docRow{}
	for _, l := range strings.Split(string(src), "\n") {
		m := line.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		p, _, _ := strings.Cut(m[2], "?")
		c := cols.Split(strings.TrimSpace(m[3]), -1)
		row := docRow{request: c[0]}
		if len(c) > 1 {
			row.response = c[1]
		}
		if len(c) > 2 {
			row.file = c[2]
		}
		key := m[1] + " " + p
		if _, dup := rows[key]; dup {
			t.Errorf("api/doc.go lists %s twice", key)
		}
		rows[key] = row
	}
	if len(rows) == 0 {
		t.Fatalf("%s holds no route table", path)
	}
	return rows
}

// schemaType is the Go type a signature is generated from: its name as api/doc.go writes it
// (qualified by its package outside api), and, for a type in api, the file declaring it.
type schemaType struct{ name, file string }

// schemaTypes maps each signature declared in dirs to its Go type.
func schemaTypes(t *testing.T, dirs ...string) map[string]schemaType {
	t.Helper()
	tag := regexp.MustCompile(`(?m)^//tony:schemagen=([a-z0-9-]+)[^\n]*\n(?://[^\n]*\n)*type (\w+)`)
	out := map[string]schemaType{}
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		pkg := filepath.Base(dir)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") || strings.HasSuffix(f, "_gen.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range tag.FindAllStringSubmatch(string(src), -1) {
				ty := schemaType{name: pkg + "." + m[2]}
				if pkg == "api" {
					ty = schemaType{name: m[2], file: filepath.Base(f)}
				}
				out[m[1]] = ty
			}
		}
	}
	return out
}
