package linear_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeLinear is a GraphQL endpoint that answers from fixtures.
//
// It answers `errors` with HTTP 200 when told to fail, which is GraphQL's own shape for a
// refused request and the one this source has to get right: read as an answer, an expired
// token becomes "the team has no issues".
type fakeLinear struct {
	mu sync.Mutex

	teams    []map[string]any            // key → visible teams
	issues   map[string][]map[string]any // team key → its issues, in page order
	cycles   map[string][]map[string]any // team key → its cycles
	projects map[string][]map[string]any // team key → its projects (one may be under several)

	// The out half's side: what was commented, and on what.
	comments []map[string]any // in order: issue, body, id
	noIssue  bool             // `issue(id:)` answers null, as it does for one nobody may see

	page    int    // issues per page (0 = all in one)
	failure string // when set, every query answers with this GraphQL error
	queries int
}

func newFake() *fakeLinear {
	return &fakeLinear{
		issues:   map[string][]map[string]any{},
		cycles:   map[string][]map[string]any{},
		projects: map[string][]map[string]any{},
	}
}

func (f *fakeLinear) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeLinear) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries
}

func (f *fakeLinear) team(key, name string) {
	f.teams = append(f.teams, map[string]any{
		"id": "team-" + key, "key": key, "name": name, "private": false,
	})
}

// project adds one to a team. The SAME project may be added to several teams, which is the
// whole point of it: a Linear project is one object shared by its teams, not a per-team view.
func (f *fakeLinear) project(key, id, name string) {
	f.projects[key] = append(f.projects[key], map[string]any{
		"id": id, "name": name, "state": "started", "progress": 0.5,
		"url": "https://linear.app/acme/project/" + id,
	})
}

// issue adds one, with the shape the query asks for.
func (f *fakeLinear) issue(key, ident, title, state, stateType string) map[string]any {
	i := map[string]any{
		"id": "uuid-" + ident, "identifier": ident, "title": title,
		"url": "https://linear.app/acme/issue/" + ident, "priority": 2,
		"priorityLabel": "Medium", "createdAt": "2026-01-01T00:00:00Z",
		"updatedAt": "2026-01-02T00:00:00Z",
		"state":     map[string]any{"name": state, "type": stateType},
		"labels":    map[string]any{"nodes": []map[string]any{}},
	}
	f.issues[key] = append(f.issues[key], i)
	return i
}

func (f *fakeLinear) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++
	if r.Header.Get("Authorization") != "lin_api_test" {
		writeJSON(w, map[string]any{"errors": []map[string]any{{"message": "Authentication failed"}}})
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if f.failure != "" {
		// HTTP 200 with errors — GraphQL's own shape for a refusal.
		writeJSON(w, map[string]any{
			"data":   nil,
			"errors": []map[string]any{{"message": f.failure}},
		})
		return
	}
	switch {
	case strings.Contains(req.Query, "issue(id:"):
		if f.noIssue {
			writeJSON(w, map[string]any{"data": map[string]any{"issue": nil}})
			return
		}
		id, _ := req.Variables["id"].(string)
		writeJSON(w, map[string]any{"data": map[string]any{
			"issue": map[string]any{"id": "uuid-" + id},
		}})
		return
	case strings.Contains(req.Query, "commentCreate"):
		issue, _ := req.Variables["issue"].(string)
		body, _ := req.Variables["body"].(string)
		id := fmt.Sprintf("comment-%d", len(f.comments)+1)
		f.comments = append(f.comments, map[string]any{"issue": issue, "body": body, "id": id})
		writeJSON(w, map[string]any{"data": map[string]any{
			"commentCreate": map[string]any{"success": true, "comment": map[string]any{
				"id": id, "url": "https://linear.app/c/" + id,
			}},
		}})
		return
	case strings.Contains(req.Query, "commentUpdate"):
		id, _ := req.Variables["id"].(string)
		body, _ := req.Variables["body"].(string)
		for _, c := range f.comments {
			if c["id"] == id {
				c["body"] = body
			}
		}
		writeJSON(w, map[string]any{"data": map[string]any{
			"commentUpdate": map[string]any{"success": true},
		}})
		return
	}
	// The teams-only query (the startup check) carries no variables.
	if !strings.Contains(req.Query, "issues(") {
		writeJSON(w, map[string]any{"data": map[string]any{
			"teams": map[string]any{"nodes": f.teams},
		}})
		return
	}
	key, _ := req.Variables["key"].(string)
	var self map[string]any
	for _, t := range f.teams {
		if t["key"] == key {
			self = t
		}
	}
	if self == nil {
		writeJSON(w, map[string]any{"data": map[string]any{
			"teams": map[string]any{"nodes": []map[string]any{}},
		}})
		return
	}
	all := f.issues[key]
	from := 0
	if after, ok := req.Variables["after"].(string); ok && after != "" {
		fmt.Sscanf(after, "cursor-%d", &from)
	}
	to := len(all)
	if f.page > 0 && from+f.page < to {
		to = from + f.page
	}
	node := map[string]any{}
	for k, v := range self {
		node[k] = v
	}
	node["cycles"] = map[string]any{"nodes": f.cycles[key]}
	node["projects"] = map[string]any{"nodes": f.projects[key]}
	node["issues"] = map[string]any{
		"pageInfo": map[string]any{
			"hasNextPage": to < len(all),
			"endCursor":   fmt.Sprintf("cursor-%d", to),
		},
		"nodes": all[from:to],
	}
	writeJSON(w, map[string]any{"data": map[string]any{
		"teams": map[string]any{"nodes": []map[string]any{node}},
	}})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
