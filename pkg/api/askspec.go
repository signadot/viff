package api

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.viff.dev/viff/pkg/entity"
)

// AskSpec is a new ask as a person says it: the words `viff ask`'s flags, viff_ask_create and
// the viff system's viff-ask-create take, its `on` already read. One grammar for all three, so a
// task asks as its owner would.
type AskSpec struct {
	Name, Question, To, Due, OnSilence, About, Keep, Label string
	On                                                     entity.Payload
}

// NewAsk is the body POST /asks takes for s, its due read against now. An error names the field
// it is about (to, due, label).
func NewAsk(s AskSpec, now time.Time) (Ask, error) {
	body := Ask{Name: s.Name, Question: s.Question, About: s.About, Keep: s.Keep,
		OnSilence: s.OnSilence, On: s.On}
	var err error
	if s.Label != "" {
		if body.Labels, err = LabelsOf(s.Label); err != nil {
			return body, err
		}
	}
	if s.To != "" {
		if body.To, err = AnswerersOf(s.To); err != nil {
			return body, err
		}
	}
	if s.Due != "" {
		if body.Due, err = DueOf(s.Due, now); err != nil {
			return body, err
		}
	}
	return body, nil
}

// AskRaised is what raising an ask answers, by `viff ask <name> -q`, viff_ask_create and the
// viff system's viff-ask-create alike: the id viff gave it, and its question.
func AskRaised(id, question string) string {
	return fmt.Sprintf("raised question %s: %s", id, question)
}

// AskQuery is the GET /asks query that keeps only the caller's own asks arg names, by the id or
// the name it was asked under: how `viff ask <name|id>`, viff_ask_get and viff-ask-get find
// one ask without being answered every ask.
func AskQuery(arg string) url.Values { return url.Values{"ask": {arg}} }

// LabelsOf reads an ask's labels: name=value pairs, comma-separated. A pair without its `=`, or an
// empty one, is refused.
func LabelsOf(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("label %q: each label is name=value", pair)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

// AnswerersOf reads whom an ask is put to: comma-separated names in the grammar a grant uses, each
// spelled canonically. An empty name is refused. Whether a group named stands is the posing's to
// check, where the store is asked.
func AnswerersOf(s string) ([]string, error) {
	var out []string
	for _, n := range strings.Split(s, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			return nil, fmt.Errorf("to %q names an empty answerer", s)
		}
		g, err := entity.ParseGrantee(n)
		if err != nil {
			return nil, fmt.Errorf("to: %v", err)
		}
		out = append(out, g.String())
	}
	return out, nil
}

// DueOf reads an ask's due: an RFC 3339 date, or a duration from now. The ask always holds a date,
// which is what its clock reads, and one not after now is refused.
func DueOf(s string, now time.Time) (string, error) {
	s = strings.TrimSpace(s)
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		d, derr := time.ParseDuration(s)
		if derr != nil {
			return "", fmt.Errorf("due %q: an RFC 3339 date (2026-10-02T17:00:00Z) or a duration from now (48h)", s)
		}
		at = now.Add(d)
	}
	if !at.After(now) {
		return "", fmt.Errorf("due %s is not in the future", at.UTC().Format(time.RFC3339))
	}
	return at.UTC().Format(time.RFC3339), nil
}
