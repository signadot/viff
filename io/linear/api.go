package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	verseio "github.com/signadot/verse/io"
)

// Linear's GraphQL API, as much of it as a reflection needs.
//
// encoding/json rather than codec, and this file is on scripts/check-json.sh's allowlist for
// the reason runner/runnerd/verserunner/agent/llm.go is: it is somebody else's wire format,
// and re-rendering it through codec would buy nothing but a second place for it to be wrong.
//
// One endpoint, one verb, three queries. A generated GraphQL client would be a build step and
// a schema checkout to keep current, for a surface small enough to read in one screen.

// APIBase is the GraphQL endpoint. Overridable so a test can point at an httptest server.
const APIBase = "https://api.linear.app/graphql"

type api struct {
	// auth is the Authorization header VERBATIM, which is what lets one field carry both of
	// Linear's mechanisms: a personal API key is sent bare (`lin_api_…`) and an OAuth access
	// token is sent as `Bearer lin_oauth_…`. Guessing which from the shape of the string is
	// exactly the kind of cleverness that fails on the day Linear adds a third prefix.
	auth string
	url  string
	cl   *http.Client
}

func newAPI(auth, base string, cl *http.Client) *api {
	if base == "" {
		base = APIBase
	}
	if cl == nil {
		cl = &http.Client{Timeout: 60 * time.Second}
	}
	return &api{auth: auth, url: base, cl: cl}
}

// query runs one GraphQL query and decodes `data` into out.
//
// THE TRAP THIS EXISTS TO AVOID is GraphQL's own: a request that failed answers HTTP 200 with
// `{"data": null, "errors": [...]}`. A client that checked the status and decoded the rest
// would read an expired token as a workspace with no issues in it — and an empty reflection
// carrying covers is a legitimate instruction to tombstone everything under them. So `errors`
// is checked on every call, in one place, and nothing here can forget to.
func (a *api) query(ctx context.Context, q string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	req.Header.Set("Authorization", a.auth)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.cl.Do(req)
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	// Named, because it is the answer a source gets for asking too often, it fixes itself, and
	// reading it as anything else sends an operator looking at their API key. See
	// verseio.Throttle for the side that prevents it — Linear's budget is hourly, so a source
	// that spends it in the first ten minutes reflects nothing for the next fifty.
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("linear: rate limited (reset %s)",
			resp.Header.Get("X-RateLimit-Requests-Reset"))
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code      string `json:"code"`
				UserError bool   `json:"userError"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	undecodable := json.Unmarshal(raw, &env)
	limited, refused := false, false
	for _, e := range env.Errors {
		limited = limited || e.Extensions.Code == "RATELIMITED"
		refused = refused || e.Extensions.UserError
	}
	if limited {
		return fmt.Errorf("linear: rate limited (reset %s)", resp.Header.Get("X-RateLimit-Requests-Reset"))
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("linear: %s: %s", resp.Status, snippet(raw))
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout {
			return verseio.Permanent(err) // a 4xx is not asked again (verseio.Permanent)
		}
		return err
	}
	if undecodable != nil {
		return fmt.Errorf("linear: undecodable answer: %w", undecodable)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		err := fmt.Errorf("linear: %s", strings.Join(msgs, "; "))
		// A REFUSAL, where Linear says the error is the request's (`userError`); one it does not
		// say that of is asked again, as before.
		if refused {
			return verseio.Permanent(err)
		}
		return err
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("linear: undecodable data: %w", err)
	}
	return nil
}

func snippet(b []byte) string {
	const max = 200
	s := string(bytes.TrimSpace(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// ── the shapes, and only the fields a reflection carries ─────────────────────

type team struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Private     bool   `json:"private"`
}

type cycle struct {
	ID          string `json:"id"`
	Number      int    `json:"number"`
	Name        string `json:"name"`
	StartsAt    string `json:"startsAt"`
	EndsAt      string `json:"endsAt"`
	CompletedAt string `json:"completedAt"`
}

type issue struct {
	ID            string   `json:"id"`
	Identifier    string   `json:"identifier"`
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Priority      int      `json:"priority"`
	PriorityLabel string   `json:"priorityLabel"`
	Estimate      *float64 `json:"estimate"`
	CreatedAt     string   `json:"createdAt"`
	UpdatedAt     string   `json:"updatedAt"`
	CompletedAt   string   `json:"completedAt"`
	State         struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Assignee *struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
	} `json:"assignee"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Project *struct {
		Name string `json:"name"`
	} `json:"project"`
	Cycle *struct {
		Number int `json:"number"`
	} `json:"cycle"`
	Parent *struct {
		Identifier string `json:"identifier"`
	} `json:"parent"`
}

// project is a Linear project as one team sees it. Every field here is GLOBAL — a project is
// one object shared by its teams, not a per-team view of one — which is exactly why it has no
// positional owner and why what this source writes goes in a slot rather than at the top of the
// entity. See Linear.Owns.
type project struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	State       string  `json:"state"`
	Progress    float64 `json:"progress"`
	StartDate   string  `json:"startDate"`
	TargetDate  string  `json:"targetDate"`
	URL         string  `json:"url"`
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// ── the two queries ──────────────────────────────────────────────────────────

const teamsQuery = `query { teams(first: 250) { nodes { id key name description private } } }`

// teams lists what this credential can see. It is the startup check — a bad token fails the
// task rather than every reflect — and it is what turns a configured set of team keys into
// an ownership claim that names real teams.
func (a *api) teams(ctx context.Context) ([]team, error) {
	var out struct {
		Teams struct {
			Nodes []team `json:"nodes"`
		} `json:"teams"`
	}
	if err := a.query(ctx, teamsQuery, nil, &out); err != nil {
		return nil, err
	}
	return out.Teams.Nodes, nil
}

// teamQuery reads one team whole: itself, its cycles, and a page of its issues.
//
// ONE QUERY rather than three, which is the one thing GraphQL is unambiguously good at: a
// team's reflection is a single request against a budget that counts requests. Paging re-runs
// it with $after, so the team and its cycles are re-read on each page after the first — a team
// small enough to fit one page pays nothing for that, and one large enough not to has bigger
// problems than a duplicated cycles list.
const teamQuery = `query($key: String!, $after: String, $page: Int!) {
  teams(filter: {key: {eq: $key}}, first: 1) {
    nodes {
      id key name description private
      cycles(first: 50) { nodes { id number name startsAt endsAt completedAt } }
      projects(first: 100) { nodes { id name description state progress startDate targetDate url } }
      issues(first: $page, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id identifier title url priority priorityLabel estimate
          createdAt updatedAt completedAt
          state { name type }
          assignee { name displayName email }
          labels(first: 20) { nodes { name } }
          project { name }
          cycle { number }
          parent { identifier }
        }
      }
    }
  }
}`

type teamPage struct {
	team
	Cycles struct {
		Nodes []cycle `json:"nodes"`
	} `json:"cycles"`
	Projects struct {
		Nodes []project `json:"nodes"`
	} `json:"projects"`
	Issues struct {
		PageInfo pageInfo `json:"pageInfo"`
		Nodes    []issue  `json:"nodes"`
	} `json:"issues"`
}

// readTeam reads one team to exhaustion: every issue, every cycle.
//
// TO EXHAUSTION, and the cap is an error rather than a truncation, because the answer carries
// a cover over the whole team. A partial list under a complete cover is how a reflection
// tombstones what it merely failed to read — the one failure in this design that costs
// entities instead of returning an error.
func (a *api) readTeam(ctx context.Context, key string, pageSize, max int) (teamPage, error) {
	var whole teamPage
	after := ""
	for read := 0; ; {
		vars := map[string]any{"key": key, "page": pageSize}
		if after != "" {
			vars["after"] = after
		}
		var out struct {
			Teams struct {
				Nodes []teamPage `json:"nodes"`
			} `json:"teams"`
		}
		if err := a.query(ctx, teamQuery, vars, &out); err != nil {
			return teamPage{}, err
		}
		if len(out.Teams.Nodes) == 0 {
			return teamPage{}, fmt.Errorf("linear: no team with key %q", key)
		}
		page := out.Teams.Nodes[0]
		if after == "" {
			whole = page // the team and its cycles come from the first page
		} else {
			whole.Issues.Nodes = append(whole.Issues.Nodes, page.Issues.Nodes...)
		}
		read += len(page.Issues.Nodes)
		if !page.Issues.PageInfo.HasNextPage {
			return whole, nil
		}
		if read >= max {
			return teamPage{}, fmt.Errorf("linear: team %s has more than %d issues — a source "+
				"must answer completely for what it covers, so narrow this source to teams it "+
				"can read whole (or raise the cap)", key, max)
		}
		after = page.Issues.PageInfo.EndCursor
	}
}

// ── the out half's three calls ───────────────────────────────────────────────

type issueIDResp struct {
	Issue *struct {
		ID string `json:"id"`
	} `json:"issue"`
}

// issueID resolves the identifier a person types — `ENG-42` — into the uuid Linear's mutations
// take. Linear's `issue(id:)` accepts either, and answering null for an identifier nobody can see
// is how a typo and a permission both arrive: named as "no such issue", since from here they are
// the same fact.
func (a *api) issueID(ctx context.Context, identifier string) (string, error) {
	const q = `query($id: String!) { issue(id: $id) { id } }`
	var r issueIDResp
	if err := a.query(ctx, q, map[string]any{"id": identifier}, &r); err != nil {
		return "", err
	}
	if r.Issue == nil || r.Issue.ID == "" {
		return "", verseio.Permanent(fmt.Errorf("linear: no issue %s this credential can see", identifier))
	}
	return r.Issue.ID, nil
}

type commentResp struct {
	CommentCreate *struct {
		Success bool `json:"success"`
		Comment struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"comment"`
	} `json:"commentCreate"`
}

// createComment says something on an issue, and answers the comment's id and where to read it.
func (a *api) createComment(ctx context.Context, issueID, body string) (id, url string, err error) {
	const q = `mutation($issue: String!, $body: String!) {
	  commentCreate(input: {issueId: $issue, body: $body}) { success comment { id url } }
	}`
	var r commentResp
	if err := a.query(ctx, q, map[string]any{"issue": issueID, "body": body}, &r); err != nil {
		return "", "", err
	}
	if r.CommentCreate == nil || !r.CommentCreate.Success || r.CommentCreate.Comment.ID == "" {
		return "", "", fmt.Errorf("linear: the comment was not created")
	}
	return r.CommentCreate.Comment.ID, r.CommentCreate.Comment.URL, nil
}

type commentUpdateResp struct {
	CommentUpdate *struct {
		Success bool `json:"success"`
	} `json:"commentUpdate"`
}

// updateComment rewrites one this program left earlier. A comment is editable, so a changed input
// is an edit rather than a second comment: the thread stays as long as the conversation, not as
// long as the number of passes.
func (a *api) updateComment(ctx context.Context, id, body string) error {
	const q = `mutation($id: String!, $body: String!) {
	  commentUpdate(id: $id, input: {body: $body}) { success }
	}`
	var r commentUpdateResp
	if err := a.query(ctx, q, map[string]any{"id": id, "body": body}, &r); err != nil {
		return err
	}
	if r.CommentUpdate == nil || !r.CommentUpdate.Success {
		return fmt.Errorf("linear: comment %s was not updated", id)
	}
	return nil
}
