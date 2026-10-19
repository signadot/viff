package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CREATING AN ISSUE IS TWO CALLS: the team by its key, then the issue in it; the answer is the
// identifier a person types and the url. A team the credential cannot see, or a missing title or
// team, is refused by name before anything is made.
func TestCreateIssueFindsTheTeamAndFilesInIt(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch {
		case strings.Contains(in.Query, "teams("):
			calls = append(calls, "teams:"+in.Variables["key"].(string))
			if in.Variables["key"] == "ENG" {
				_, _ = w.Write([]byte(`{"data":{"teams":{"nodes":[{"id":"t1"}]}}}`))
			} else {
				_, _ = w.Write([]byte(`{"data":{"teams":{"nodes":[]}}}`))
			}
		case strings.Contains(in.Query, "issueCreate("):
			calls = append(calls, "create:"+in.Variables["team"].(string))
			_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"i1","identifier":"ENG-7","url":"https://linear.app/x/issue/ENG-7"}}}}`))
		}
	}))
	defer srv.Close()
	got, err := CreateIssue(context.Background(), "lin_api_x", srv.URL, nil, "ENG", "the roof leaks", "when it rains")
	if err != nil || got != (Created{ID: "i1", Identifier: "ENG-7", URL: "https://linear.app/x/issue/ENG-7"}) {
		t.Fatalf("%+v, %v", got, err)
	}
	if strings.Join(calls, " ") != "teams:ENG create:t1" {
		t.Errorf("calls: %v", calls)
	}
	if _, err := CreateIssue(context.Background(), "lin_api_x", srv.URL, nil, "NOPE", "x", ""); err == nil || !strings.Contains(err.Error(), "no team NOPE") {
		t.Errorf("an unknown team: %v", err)
	}
	if _, err := CreateIssue(context.Background(), "lin_api_x", srv.URL, nil, "ENG", " ", ""); err == nil || !strings.Contains(err.Error(), "title") {
		t.Errorf("no title: %v", err)
	}
	if _, err := CreateIssue(context.Background(), "lin_api_x", srv.URL, nil, "", "x", ""); err == nil || !strings.Contains(err.Error(), "team key") {
		t.Errorf("no team: %v", err)
	}
	if len(calls) != 3 {
		t.Errorf("a refused input reached Linear: %v", calls)
	}
}

// COMMENTING AND CLOSING NAME THE ISSUE AS A PERSON DOES: the identifier is resolved to the
// uuid, a comment is created on it, and a close moves it to the team's completed state; an
// issue the credential cannot see, or a team with no completed state, is refused by name.
func TestCommentAndCloseResolveTheIssueFirst(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch {
		case strings.Contains(in.Query, "states("):
			calls = append(calls, "states:"+in.Variables["id"].(string))
			switch in.Variables["id"] {
			case "ENG-42":
				_, _ = w.Write([]byte(`{"data":{"issue":{"id":"i42","team":{"key":"ENG","states":{"nodes":[{"id":"s1","name":"Todo","type":"unstarted"},{"id":"s9","name":"Done","type":"completed"}]}}}}}`))
			case "OPS-1":
				_, _ = w.Write([]byte(`{"data":{"issue":{"id":"i1","team":{"key":"OPS","states":{"nodes":[{"id":"s1","name":"Todo","type":"unstarted"}]}}}}}`))
			default:
				_, _ = w.Write([]byte(`{"data":{"issue":null}}`))
			}
		case strings.Contains(in.Query, "issue(id:"):
			calls = append(calls, "issue:"+in.Variables["id"].(string))
			if in.Variables["id"] == "ENG-42" {
				_, _ = w.Write([]byte(`{"data":{"issue":{"id":"i42"}}}`))
			} else {
				_, _ = w.Write([]byte(`{"data":{"issue":null}}`))
			}
		case strings.Contains(in.Query, "commentCreate("):
			calls = append(calls, "comment:"+in.Variables["issue"].(string))
			_, _ = w.Write([]byte(`{"data":{"commentCreate":{"success":true,"comment":{"id":"c1","url":"https://linear.app/x/issue/ENG-42#c1"}}}}`))
		case strings.Contains(in.Query, "issueUpdate("):
			calls = append(calls, "update:"+in.Variables["id"].(string)+":"+in.Variables["state"].(string))
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"identifier":"ENG-42","url":"https://linear.app/x/issue/ENG-42"}}}}`))
		}
	}))
	defer srv.Close()
	c, err := Comment(context.Background(), "k", srv.URL, nil, "ENG-42", "on it")
	if err != nil || c != (Commented{ID: "c1", URL: "https://linear.app/x/issue/ENG-42#c1"}) {
		t.Fatalf("%+v, %v", c, err)
	}
	if _, err := Comment(context.Background(), "k", srv.URL, nil, "NOPE-1", "x"); err == nil || !strings.Contains(err.Error(), "no issue NOPE-1") {
		t.Errorf("an unseen issue: %v", err)
	}
	cl, err := CloseIssue(context.Background(), "k", srv.URL, nil, "ENG-42")
	if err != nil || cl != (Closed{Identifier: "ENG-42", State: "Done", URL: "https://linear.app/x/issue/ENG-42"}) {
		t.Fatalf("%+v, %v", cl, err)
	}
	if _, err := CloseIssue(context.Background(), "k", srv.URL, nil, "OPS-1"); err == nil || !strings.Contains(err.Error(), "no completed state") {
		t.Errorf("a team with no completed state: %v", err)
	}
	if got := strings.Join(calls, " "); got != "issue:ENG-42 comment:i42 issue:NOPE-1 states:ENG-42 update:i42:s9 states:OPS-1" {
		t.Errorf("calls: %s", got)
	}
}
