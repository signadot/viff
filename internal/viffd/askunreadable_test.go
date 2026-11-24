package viffd

import (
	"testing"

	"go.viff.dev/viff/pkg/entity"
	"go.viff.dev/viff/pkg/mirror"
)

// AN ASK WITH ONE BAD KEY IS STILL ITS ASKER'S. A record that does not read as an api.AskRecord
// is still told a person's ask by its `asker`, so a restart still waits on it and its asker can
// still withdraw it, which is when they most want it gone.
func TestAnAskThatDoesNotReadIsStillItsAskers(t *testing.T) {
	srv, ts := asking(t)
	const victor = "user:victor@example.com"
	id := askID(t, ask(t, victor, ts.URL, `{name: land, question: "Merge?", to: [oscar@example.com]}`))
	ref := mirror.AskRef(id)
	posed(t, srv, ref)
	// An edit that is not a number: the record no longer reads as an api.AskRecord.
	if _, _, err := entity.As(srv.Store, entity.SystemDocket).Write(entity.Write{
		At: ref.At(), Value: entity.Obj{"edit": "two"}.Payload(),
	}); err != nil {
		t.Fatal(err)
	}
	e, _, _ := srv.Store.Get(ref)
	if !personsAsk(e.Payload) {
		t.Fatal("an ask that does not read is not taken for a person's ask")
	}
	resp := deleteAs(t, victor, ts.URL+"/asks/"+id)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("its asker withdrawing it = %s, want it withdrawn", resp.Status)
	}
	// The record is gone, and with it the bad key: nothing for the check at the test's end.
	if _, ok, _ := srv.Store.Get(ref); ok {
		t.Fatal("the ask still stands after its asker withdrew it")
	}
}
