package linear

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	viffio "go.viff.dev/viff/pkg/io"
)

// Filing, commenting on and closing an issue, for a runner's `linear-issue-*` tools: the Linear
// writes viff makes that are not a reflection's. They live here because the GraphQL wire format
// does (api.go), and a second client would be a second place for the error envelope to be read
// wrong.

// Created is the issue Linear answered with: what the caller reads back, and where to find it.
type Created struct {
	ID         string // Linear's uuid
	Identifier string // what a person types: `ENG-42`
	URL        string
}

// CreateIssue files an issue in the team whose key is teamKey (`ENG`), with the title and the
// description given, as the credential auth (the Authorization header, verbatim). base is the
// GraphQL endpoint, "" for Linear's own; hc nil is a default client. A team the credential cannot
// see is refused by name, since from here a typo and a permission are the same fact.
func CreateIssue(ctx context.Context, auth, base string, hc *http.Client, teamKey, title, description string) (Created, error) {
	a := newAPI(auth, base, hc)
	teamKey = strings.TrimSpace(teamKey)
	if teamKey == "" {
		return Created{}, fmt.Errorf("linear: a team key is required (ENG, …)")
	}
	if strings.TrimSpace(title) == "" {
		return Created{}, fmt.Errorf("linear: a title is required")
	}
	var teams struct {
		Teams struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	const teamByKey = `query($key: String!) { teams(filter: {key: {eq: $key}}, first: 1) { nodes { id } } }`
	if err := a.query(ctx, teamByKey, map[string]any{"key": teamKey}, &teams); err != nil {
		return Created{}, err
	}
	if len(teams.Teams.Nodes) == 0 {
		return Created{}, fmt.Errorf("linear: no team %s this credential can see", teamKey)
	}
	var r struct {
		IssueCreate *struct {
			Success bool `json:"success"`
			Issue   struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				URL        string `json:"url"`
			} `json:"issue"`
		} `json:"issueCreate"`
	}
	const create = `mutation($team: String!, $title: String!, $description: String) {
	  issueCreate(input: {teamId: $team, title: $title, description: $description}) {
	    success issue { id identifier url }
	  }
	}`
	vars := map[string]any{"team": teams.Teams.Nodes[0].ID, "title": title, "description": description}
	if err := a.query(ctx, create, vars, &r); err != nil {
		return Created{}, err
	}
	if r.IssueCreate == nil || !r.IssueCreate.Success || r.IssueCreate.Issue.Identifier == "" {
		return Created{}, fmt.Errorf("linear: the issue was not created")
	}
	return Created{ID: r.IssueCreate.Issue.ID, Identifier: r.IssueCreate.Issue.Identifier, URL: r.IssueCreate.Issue.URL}, nil
}

// Commented is the comment Linear answered with, and where to read it.
type Commented struct {
	ID  string
	URL string
}

// Comment says something on an issue a person names (`ENG-42`), as the credential auth. An
// issue the credential cannot see is refused by name.
func Comment(ctx context.Context, auth, base string, hc *http.Client, identifier, body string) (Commented, error) {
	a := newAPI(auth, base, hc)
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return Commented{}, fmt.Errorf("linear: an issue identifier is required (ENG-42)")
	}
	if strings.TrimSpace(body) == "" {
		return Commented{}, fmt.Errorf("linear: a comment needs a body")
	}
	id, err := a.issueID(ctx, identifier)
	if err != nil {
		return Commented{}, err
	}
	cid, url, err := a.createComment(ctx, id, body)
	if err != nil {
		return Commented{}, err
	}
	return Commented{ID: cid, URL: url}, nil
}

// Closed is what closing an issue came to: the state it is in now, by name.
type Closed struct {
	Identifier string
	State      string
	URL        string
}

// CloseIssue moves an issue a person names (`ENG-42`) to its team's first state of type
// `completed` — Linear has no close, only states, and "completed" is the type its workflows
// mark done with — as the credential auth. A team with no such state is refused by name.
func CloseIssue(ctx context.Context, auth, base string, hc *http.Client, identifier string) (Closed, error) {
	a := newAPI(auth, base, hc)
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return Closed{}, fmt.Errorf("linear: an issue identifier is required (ENG-42)")
	}
	var found struct {
		Issue *struct {
			ID   string `json:"id"`
			Team struct {
				Key    string `json:"key"`
				States struct {
					Nodes []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
						Type string `json:"type"`
					} `json:"nodes"`
				} `json:"states"`
			} `json:"team"`
		} `json:"issue"`
	}
	const q = `query($id: String!) { issue(id: $id) { id team { key states(first: 50) { nodes { id name type } } } } }`
	if err := a.query(ctx, q, map[string]any{"id": identifier}, &found); err != nil {
		return Closed{}, err
	}
	if found.Issue == nil || found.Issue.ID == "" {
		return Closed{}, viffio.Permanent(fmt.Errorf("linear: no issue %s this credential can see", identifier))
	}
	var stateID, stateName string
	for _, s := range found.Issue.Team.States.Nodes {
		if s.Type == "completed" {
			stateID, stateName = s.ID, s.Name
			break
		}
	}
	if stateID == "" {
		return Closed{}, fmt.Errorf("linear: team %s has no completed state to close %s into", found.Issue.Team.Key, identifier)
	}
	var r struct {
		IssueUpdate *struct {
			Success bool `json:"success"`
			Issue   struct {
				Identifier string `json:"identifier"`
				URL        string `json:"url"`
			} `json:"issue"`
		} `json:"issueUpdate"`
	}
	const m = `mutation($id: String!, $state: String!) {
	  issueUpdate(id: $id, input: {stateId: $state}) { success issue { identifier url } }
	}`
	if err := a.query(ctx, m, map[string]any{"id": found.Issue.ID, "state": stateID}, &r); err != nil {
		return Closed{}, err
	}
	if r.IssueUpdate == nil || !r.IssueUpdate.Success {
		return Closed{}, fmt.Errorf("linear: %s was not closed", identifier)
	}
	return Closed{Identifier: r.IssueUpdate.Issue.Identifier, State: stateName, URL: r.IssueUpdate.Issue.URL}, nil
}
