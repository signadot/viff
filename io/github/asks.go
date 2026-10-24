package github

import (
	"context"
	"fmt"
	stdio "io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/signadot/tony-format/go-tony/ir"

	verseapi "github.com/signadot/verse/api"
	"github.com/signadot/verse/client"
	"github.com/signadot/verse/entity"
	verseio "github.com/signadot/verse/io"
	"github.com/signadot/verse/mirror"
)

// A question asked on a pull request, and answered there (git-issue 934azhtjh12kr7mtqsn0 step 5;
// zvf8y5n7h12kr5tgr1n0 S3).
//
// An input carrying `asks` names questions, and the comment this io keeps for it says what each
// asks and how to answer: a comment saying `/verse <option>` on a line of its own. Each pass
// reads the replies on the pull request — the comments after this io's own, as the in half
// reflects them under the repository's place — and answers one AS ITS AUTHOR: the author's GitHub account is looked
// up for who they are (docs/reference/perms.md, "accounts in other systems"), and the answer is
// made as that person with the account beside it, which a verse that verifies people admits
// through the binding and records on the ballot. It is never made as this io: an answer recorded
// as agent:github-iod would be an answer nobody gave.
//
// A reply this io cannot act on — by somebody verse does not know, or naming no option the
// question offers — is told so once, in a reply of its own, and not again: every reply acted on
// is on the status under `handled`, by its comment id.
const commandWord = "/verse"

// asked is one question the comment is about, as the status keeps it between passes: the edit
// the comment showed, which is what a reply consents to, and the options it offered.
type asked struct {
	ID      string
	Options []string
	Edit    int
	Settled bool
}

// editsOf is what the comment shows of each question's edit, as the status keeps it: `edits:
// {<id>: <edit>}`.
func editsOf(asks []asked) entity.Obj {
	out := entity.Obj{}
	for _, a := range asks {
		out[a.ID] = a.Edit
	}
	return out
}

// shownEdits is the questions with the edit the comment showed before this pass, where the status
// has one; a question the comment never showed is judged at the edit it has.
func shownEdits(asks []asked, status entity.Payload) []asked {
	edits := entity.Get(status, "edits")
	out := make([]asked, len(asks))
	for i, a := range asks {
		if n := ir.Uncomment(entity.Get(edits, a.ID)); n != nil && n.Int64 != nil {
			a.Edit = int(*n.Int64)
		}
		out[i] = a
	}
	return out
}

// question is what the comment says of a question, as its record says it.
type question struct {
	id, pose, about, status, verdict, by string
	edit                                 int
	opts                                 []string
	standing                             bool
}

// asksBody is the comment's text: what the input says, then each question and how to answer it.
func (k *Sink) asksBody(ctx context.Context, text string, ids []string) (string, []asked, error) {
	var lines []string
	if text != "" {
		lines = append(lines, text)
	}
	var out []asked
	for _, id := range ids {
		q, err := k.question(id)
		if err != nil {
			return "", nil, err
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		if !q.standing {
			lines = append(lines, "> `"+id+"` is no question this io can read.")
			continue
		}
		lines = append(lines, "> "+q.pose)
		if q.about != "" {
			lines = append(lines, "> _"+q.about+"_")
		}
		settled := q.status != "" && q.status != mirror.QuestionPending
		switch {
		case settled && q.by != "":
			lines = append(lines, fmt.Sprintf("> **%s**, by %s.", q.verdict, strings.TrimPrefix(q.by, "user:")))
		case settled:
			lines = append(lines, fmt.Sprintf("> **%s**.", q.verdict))
		default:
			opts := make([]string, len(q.opts))
			for i, o := range q.opts {
				opts[i] = "`" + commandWord + " " + o + "`"
			}
			lines = append(lines, "> Answer with a comment saying "+strings.Join(opts, " or ")+
				" on a line of its own. Words after the option are kept with your answer.")
		}
		out = append(out, asked{ID: id, Options: q.opts, Edit: q.edit, Settled: settled})
	}
	return strings.Join(lines, "\n"), out, nil
}

// question reads a question's record as this io, which reads what a rule's gate holds out.
func (k *Sink) question(id string) (question, error) {
	q := question{id: id}
	if k.Verse == nil {
		return q, verseio.Permanent(fmt.Errorf("this io was given no verse to read question %s from", id))
	}
	doc, err := verseio.ReadAt(k.Verse, mirror.QuestionRef(id).KPath())
	if err != nil {
		return q, fmt.Errorf("reading question %s: %w", id, err)
	}
	if doc == nil {
		return q, nil
	}
	q.standing = true
	q.pose, q.about = entity.Str(doc, "pose"), entity.Str(doc, "about")
	if q.pose == "" {
		q.pose = entity.Str(doc, "reason")
	}
	if q.pose == "" {
		q.pose = id
	}
	q.status, q.verdict, q.by = entity.Str(doc, "status"), entity.Str(doc, "verdict"), entity.Str(doc, "by")
	q.edit = mirror.EditOf(doc)
	// OptionsOf answers the default pair for a record naming none.
	for _, o := range mirror.OptionsOf(doc) {
		q.opts = append(q.opts, o.ID)
	}
	return q, nil
}

// answerReplies reads the replies on the pull request after this io's own comment and acts on
// each once: answers it as its author, or tells the author why it cannot. It answers the status
// fields to merge: `handled`, the ids of the replies acted on so far, in the order acted on.
func (k *Sink) answerReplies(ctx context.Context, repo string, number int, posted int64,
	asks []asked, status entity.Payload) (entity.Obj, error) {

	pending := false
	for _, a := range asks {
		if !a.Settled {
			pending = true
		}
	}
	if !pending {
		return nil, nil // nothing left to answer; what is said in the thread stays GitHub's
	}
	handled := verseio.Strs(entity.Get(status, "handled"))
	// A failure of an earlier pass that stands on the status is cleared by a pass that reads
	// the replies and finds nothing to act on: nothing written would leave it standing.
	recovered := entity.Str(status, "error") != ""
	done := map[string]bool{}
	for _, id := range handled {
		done[id] = true
	}
	self, err := k.g.api.self(ctx)
	if err != nil {
		return nil, err
	}
	replies, err := k.replies(repo, number)
	if err != nil {
		return nil, err
	}
	var acted []string
	for _, c := range replies {
		id := strconv.FormatInt(c.id, 10)
		if c.id <= posted || done[id] || strings.EqualFold(c.login, self) {
			continue
		}
		if nonce, ok := linking(c.body); ok {
			told, err := k.link(ctx, repo, number, c, nonce)
			if err != nil {
				return nil, err
			}
			if told {
				acted = append(acted, id)
			}
			continue
		}
		option, say, ok := command(c.body)
		if !ok {
			continue // a comment that is not a command is conversation, and nothing of this io's
		}
		told, err := k.act(ctx, repo, number, c, option, say, asks)
		if err != nil {
			return nil, err
		}
		if told {
			acted = append(acted, id)
		}
	}
	if len(acted) == 0 && !recovered {
		return nil, nil
	}
	ids := make([]any, 0, len(handled)+len(acted))
	for _, id := range append(handled, acted...) {
		ids = append(ids, id)
	}
	return entity.Obj{"handled": ids}, nil
}

// reply is one comment on the pull request, as the in half reflects it (commentPayload).
type reply struct {
	id, userID  int64
	login, body string
}

// replies is the conversation on the pull request as this io itself reflects it under the
// repository's place, `<root>.<org>.<name>.comment.<n>.<id>`, read through the verse and never
// asked of GitHub again: the reflection is the one record of what was said, the same one a
// charter reads, and what the in half's window holds is what there is. A reply arrives in it on
// the comment's delivery, which also kicks a pass (cmd/iod), or on the resync. Oldest first, so
// that two commands are acted on in the order they were made.
func (k *Sink) replies(repo string, number int) ([]reply, error) {
	node, err := verseio.ReadAt(k.Verse, k.g.ItemRef("comment", repo, number).KPath())
	if err != nil {
		return nil, err
	}
	var out []reply
	for _, name := range verseio.Fields(node) {
		id, err := strconv.ParseInt(name, 10, 64)
		if err != nil {
			continue
		}
		c := entity.Get(node, name)
		// The conversation, which is where the io's comment is and a reply to it lands. A
		// review comment on a line of the diff is numbered from another sequence of GitHub's,
		// so it is neither ordered nor windowed with these.
		if kind := entity.Str(c, "kind"); kind != "" && kind != "conversation" {
			continue
		}
		r := reply{id: id, login: entity.Str(c, "author"), body: entity.Str(c, "body")}
		if n := ir.Uncomment(entity.Get(c, "authorId")); n != nil && n.Int64 != nil {
			r.userID = *n.Int64
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
}

// linkWord is the one `/verse` word that is never an answer: `/verse link <nonce>` is how a person
// proves a GitHub account is theirs (docs/reference/perms.md, "accounts in other systems"). It may
// be said on any issue or pull request this io reflects, where verse reads it itself when the
// person's CLI asks (`verify`); this io reports it only from a reply on a pull request it is
// asking a question on, where it reads replies anyway.
const linkWord = "link"

// linking reads the `/verse link <nonce>` line from a comment, where the line is the comment's own
// and the nonce the whole of it.
func linking(body string) (nonce string, ok bool) {
	for _, line := range strings.Split(body, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == commandWord && strings.EqualFold(f[1], linkWord) {
			return f[2], true
		}
	}
	return "", false
}

// link proves the GitHub account is the claimant's: the verse gave somebody the nonce
// (`verse account -link github -account <login>`), and this io's word that this account said it in
// a comment it reflected is what binds the two (`POST /accounts/github/heard`; perms.md, "accounts
// in other systems"). The word is the io's own, so it is given as the io itself; the author is
// told what became of it, once.
func (k *Sink) link(ctx context.Context, repo string, number int, c reply, nonce string) (bool, error) {
	tell := func(text string) (bool, error) {
		_, err := k.g.api.createComment(ctx, repo, number, "@"+c.login+" "+text)
		return err == nil, err
	}
	if c.userID == 0 {
		return tell("not linked: the comment carries no author number, so it is no evidence of whose it is.")
	}
	resp, err := k.Verse.Post("/accounts/github/heard", nil, verseapi.AccountHeard{
		Nonce: nonce, Account: "github:" + strconv.FormatInt(c.userID, 10), Login: c.login,
	})
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusInternalServerError {
		return false, client.StatusError(resp, "linking "+c.login) // the verse's trouble, not the author's: next pass
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return tell("not linked: " + client.StatusError(resp, "`"+commandWord+" "+linkWord+"`").Error())
	}
	var row struct {
		Principal string `tony:"field=principal"`
	}
	if err := client.Decode(resp, &row); err != nil {
		return false, err
	}
	return tell(fmt.Sprintf("linked: in verse, %s is %s.", c.login, row.Principal))
}

// command reads a `/verse <option> [words]` line from a comment, where the line is the command's
// own: a comment quoting somebody else's instructions is not an answer.
func command(body string) (option, say string, ok bool) {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, commandWord+" ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, commandWord))
		option, say, _ = strings.Cut(rest, " ")
		if option == "" || strings.EqualFold(option, linkWord) {
			continue // the account proof, or nothing: a later line may still command
		}
		return option, strings.TrimSpace(say), true
	}
	return "", "", false
}

// act answers one command as its author, or tells the author why it cannot. It reports whether
// the reply has been dealt with: an answer the verse refused for a reason that may pass next time
// (the question still being posed) is left for the next pass.
func (k *Sink) act(ctx context.Context, repo string, number int, c reply, option, say string, asks []asked) (bool, error) {
	tell := func(text string) (bool, error) {
		_, err := k.g.api.createComment(ctx, repo, number, "@"+c.login+" "+text)
		return err == nil, err
	}
	// The question the option answers. Where more than one pending question takes the word,
	// the first word after it may name the question, and otherwise the command is unplaceable,
	// as a Slack reply naming an option two questions share is: it is not guessed.
	a, option, say, could := placed(asks, option, say)
	if len(could) > 1 {
		ids := make([]string, len(could))
		for i, q := range could {
			ids[i] = "`" + q.ID + "`"
		}
		return tell(fmt.Sprintf("`%s %s` could answer %s: say which, `%s %s <question>`.",
			commandWord, option, strings.Join(ids, " or "), commandWord, option))
	}
	if a == nil {
		var offered []string
		for _, q := range asks {
			if !q.Settled {
				for _, o := range q.Options {
					offered = append(offered, "`"+commandWord+" "+o+"`")
				}
			}
		}
		return tell(fmt.Sprintf("`%s %s` names no option this question offers: say %s.",
			commandWord, option, strings.Join(offered, " or ")))
	}
	if k.As == nil || c.userID == 0 {
		return tell("an answer here cannot be counted: this io was not given a way to answer as you.")
	}
	who, err := k.whoIs(ctx, c.userID)
	if err != nil {
		return false, err
	}
	if who == "" {
		return tell(fmt.Sprintf("I do not know who %s is in verse, so this does not count. Claim the "+
			"account from your verse (`verse account -link github -account %s`) and say the "+
			"`%s %s <nonce>` it prints in a comment here, on a line of its own. Then say it again.",
			c.login, c.login, commandWord, linkWord))
	}
	body := verseapi.Resolve{ID: a.ID, Choice: option, By: who, Edit: a.Edit}
	if say != "" {
		body.Say = []string{say}
	}
	resp, err := k.As(who, "github:"+strconv.FormatInt(c.userID, 10)).
		Post("/asks/"+url.PathEscape(a.ID)+"/answer", nil, body)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusAccepted:
		return true, nil
	case http.StatusServiceUnavailable:
		return false, nil // still being posed: asked again next pass
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		return false, nil // the verse's trouble, not the answer's: asked again next pass
	}
	refusal, _ := stdio.ReadAll(resp.Body)
	return tell(fmt.Sprintf("the verse did not take that answer: %s", strings.TrimSpace(string(refusal))))
}

// placed is the question a command answers: the one pending question offering the option, matched
// by name and not by case, or, where several do, the one the first word after the option names.
// It answers the option as the question spells it, the words left after the question's name, and
// the questions that could take the option where none was named, which is how an ambiguous
// command is told its choices.
func placed(asks []asked, option, say string) (a *asked, canon, rest string, could []*asked) {
	canon = option
	for i := range asks {
		if asks[i].Settled {
			continue
		}
		for _, o := range asks[i].Options {
			if strings.EqualFold(o, option) {
				could = append(could, &asks[i])
				canon = o
			}
		}
	}
	switch {
	case len(could) == 1:
		return could[0], canon, say, nil
	case len(could) > 1:
		named, more, _ := strings.Cut(say, " ")
		for _, q := range could {
			if q.ID == named {
				return q, canon, strings.TrimSpace(more), nil
			}
		}
	}
	return nil, canon, say, could
}

// whoIs is the principal a GitHub account is bound to, "" where no binding this io may read says.
func (k *Sink) whoIs(ctx context.Context, userID int64) (string, error) {
	resp, err := k.Verse.Get("/accounts/github/who", url.Values{"sub": {strconv.FormatInt(userID, 10)}})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", client.StatusError(resp, fmt.Sprintf("who is github account %d", userID))
	}
	var row struct {
		Principal string `tony:"field=principal"`
	}
	if err := client.Decode(resp, &row); err != nil {
		return "", err
	}
	return row.Principal, nil
}
