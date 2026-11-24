package viffd

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"go.viff.dev/viff/pkg/api"
	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/runner/client"
	"go.viff.dev/viff/pkg/runner/runnerd"
)

// THE LOCAL RUNNER is the runnerd `viff up` starts beside itself, registered like any other
// runner: under the name `local`, proved by a static token kept in the data dir, described by its
// hello and handshaken before each launch. Nothing about it is special to routing; what is
// special is only who writes its registration, and that `viff up` is the one that stops it.

// LocalRunnerName is the registration `viff up` writes for the runner it starts: reserved, so
// `viff runner add local` is refused to anybody but the system.
const LocalRunnerName = "local"

// RegisterLocalRunner writes the local runner's registration, as the system, naming url and the
// subject of its static token. A registration that already says so is left as it stands, its
// description with it; one that says anything else is written whole, and described then and there
// where the runner already answers (one a crashed run left), or by its hello once it starts.
func (s *Server) RegisterLocalRunner(url, subject string) error {
	e := api.Runner{Name: LocalRunnerName, URL: url, Subject: subject}
	if err := e.Check(); err != nil {
		return err
	}
	cur, ok, err := findRunnerErr(s.Store, LocalRunnerName)
	if err != nil {
		return errors.New(notRead("the local runner's registration", err))
	}
	s.localRunner = true
	if ok && cur.URL == url && cur.Subject == subject && cur.Identity == "" {
		return nil
	}
	if _, _, err := s.Store.Write(entity.Write{At: runnerEntityRef(LocalRunnerName).At(), Value: entity.Obj{
		"name": LocalRunnerName, "url": url, "subject": subject, "identity": entity.Removed(),
		"described": entity.Removed(), "offers": entity.Removed(), "verified": entity.Removed(), "unverified": entity.Removed(),
	}.Payload()}); err != nil {
		return err
	}
	s.forgetRegistrations()
	_, _ = s.handshake(e) // a runner not answering yet is described by its hello
	return nil
}

// RemoveLocalRunner removes the local runner's registration, as `viff up` does when it stops it.
func (s *Server) RemoveLocalRunner() error {
	if _, _, err := s.Store.Write(entity.Write{At: runnerEntityRef(LocalRunnerName).At(), Value: nil}); err != nil {
		return err
	}
	s.forgetRegistrations()
	return nil
}

// LocalRunnerAt is whether what answers at url is the local runner: a runner that proves it holds
// the token whose hash is subject, to this viff. It is how `viff up` finds the runner a crashed
// run left behind, and uses it rather than start a second one.
func (s *Server) LocalRunnerAt(url, subject string) (bool, error) {
	reach, _, why := s.probeRegistered(api.Runner{Name: LocalRunnerName, URL: url, Subject: subject})
	return reach == client.IsRunner, why
}

// StopRunnerAt asks the runner at url, which proves subject, to stop (runnerd.StopPath), with a
// token this viff instance mints for it.
func (s *Server) StopRunnerAt(url, subject string) error {
	c := &http.Client{Transport: s.runnerTransport(subject)}
	resp, err := c.Post(strings.TrimRight(url, "/")+runnerd.StopPath, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s answered %s: %s", url, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// localRunnerStatus is what `viff status` says of the local runner, asked now and never
// stored: its registration, whether it is described and what it offers, and where it is not,
// why not, from the runner itself.
func (s *Server) localRunnerStatus() *api.RunnerStatus {
	if !s.localRunner {
		return nil
	}
	out := &api.RunnerStatus{At: runnerEntityRef(LocalRunnerName).String(), Offers: []string{}}
	e, ok, err := findRunnerErr(s.Store, LocalRunnerName)
	switch {
	case err != nil:
		out.Why = notRead("its registration", err)
		return out
	case !ok:
		out.Why = "it is not registered"
		return out
	}
	out.URL = e.URL
	if e.Current() {
		out.Described, out.Offers = true, e.OfferNames()
		return out
	}
	reach, _, why := s.probeRegistered(e)
	out.Why = "not described"
	if why != nil {
		out.Why = fmt.Sprintf("not described: %s (%v)", reach, why)
	} else if reach == client.IsRunner {
		out.Why = "not described yet: it answers, and has not said hello since it was registered (`viff runner check local`)"
	}
	return out
}
