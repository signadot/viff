package entity

import (
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	docdserver "github.com/signadot/tony-format/go-tony/system/docd/server"
	logdserver "github.com/signadot/tony-format/go-tony/system/logd/server"
	"github.com/signadot/tony-format/go-tony/system/logd/storage"
)

// Does a store whose connection died come back on its own?
//
// A deployment said no. `make deploy` rolled docd and viffd together, docd's volume took
// 24s to detach from the old node, and from then on every write viffd made timed out —
// for five and a half minutes, until a human restarted the pod. Nothing was broken except
// a session.
//
// These tests are the reproduction, and they are written to distinguish TWO failures that
// wear one costume. A connection can end by being CLOSED, where the peer sends a FIN and
// the read-pump learns in microseconds, or by going DARK, where the route stops carrying
// packets and nothing is ever delivered — no FIN, no reset, no error, just a socket that
// looks open from both ends forever. The first is a notification; the second can only be
// noticed by waiting. A recovery built against the first and shipped against the second is
// exactly the outage, since a rolled pod does not always get to say goodbye.
//
// The assertion in both is viff's own observable behaviour and nothing about the
// substrate's mechanism: after connectivity returns, A WRITE SUCCEEDS. Not "the session
// reports connected", not "a reconnect was attempted" — the store does the thing it exists
// to do. What layer makes that true is free to change.

// gate is a TCP proxy standing between a store and docd, so a test can take the connection
// away in a chosen manner and give it back.
//
// It exists because both failures are about the ROUTE rather than the peer: stopping docd
// tests a store against a store that is gone, which is a different (and easier) thing than
// a store that is fine and unreachable. Holding the backend still and cutting the wire
// isolates the session, which is what is under test.
type gate struct {
	t       *testing.T
	backend string
	ln      net.Listener

	mu   sync.Mutex
	live []net.Conn
	dark bool
}

func newGate(t *testing.T, backend string) *gate {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	g := &gate{t: t, backend: backend, ln: ln}
	go g.serve()
	t.Cleanup(func() {
		ln.Close()
		g.cut()
	})
	return g
}

func (g *gate) addr() string { return g.ln.Addr().String() }

func (g *gate) serve() {
	for {
		c, err := g.ln.Accept()
		if err != nil {
			return
		}
		go g.handle(c)
	}
}

func (g *gate) handle(c net.Conn) {
	if g.isDark() {
		// Accepted, and connected to nothing. A blackholed route completes a TCP dial
		// against whatever is still holding the address and then swallows everything,
		// so a client that redials during the outage has a socket, a successful
		// connect, and no service — which is the state that makes this failure hard.
		g.track(c)
		return
	}
	b, err := net.Dial("tcp", g.target())
	if err != nil {
		c.Close()
		return
	}
	g.track(c, b)
	go g.pipe(b, c)
	go g.pipe(c, b)
}

// pipe copies until the connection ends or the gate goes dark. Going dark stops the
// copying and leaves BOTH sockets open, which is the whole point: the bytes stop and
// nobody is told. A response already in flight is swallowed here.
func (g *gate) pipe(dst, src net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if g.isDark() {
				return
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				return
			}
			return
		}
	}
}

func (g *gate) track(conns ...net.Conn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.live = append(g.live, conns...)
}

func (g *gate) isDark() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.dark
}

// cut ends every live connection, the way a peer that closes cleanly does. The listener
// keeps accepting, so a redial works immediately.
func (g *gate) cut() {
	g.mu.Lock()
	live := g.live
	g.live = nil
	g.mu.Unlock()
	for _, c := range live {
		c.Close()
	}
}

// dark stops carrying packets without closing anything, in either direction, on the
// connections that exist and on any opened while it lasts.
func (g *gate) darken() {
	g.mu.Lock()
	g.dark = true
	g.mu.Unlock()
}

// point sends connections opened from now on to a different backend — the Service moving
// to a new pod, from a client that never learns the address changed.
func (g *gate) point(backend string) {
	g.mu.Lock()
	g.backend = backend
	g.mu.Unlock()
}

func (g *gate) target() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.backend
}

// heal resumes service for connections opened from now on. The ones darkened stay dead,
// as the old pod's socket does — recovery has to come from a NEW connection, which is the
// part the store is responsible for.
func (g *gate) heal() {
	g.mu.Lock()
	g.dark = false
	g.mu.Unlock()
}

// storeThroughGate stands up logd + docd (the production shape: viff talks to docd's
// client face) with a gate in front, and returns the store and its gate.
func storeThroughGate(t *testing.T) (*LogdStore, *gate) {
	t.Helper()
	docd := startDocd(t, startLogd(t))
	g := newGate(t, docd.ClientTCPAddr())
	return storeVia(t, g), g
}

func storeVia(t *testing.T, g *gate) *LogdStore {
	t.Helper()
	s, err := OpenLogdStore(LogdConfig{Addr: g.addr(), ClientID: "viff-test"})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// tier is one pod's worth of substrate: logd and the docd in front of it, over a data
// directory that outlives them. Two tiers over one directory is a restart.
type tier struct {
	logd *logdserver.Server
	docd *docdserver.Server
}

func startTier(t *testing.T, dir string) *tier {
	t.Helper()
	st, err := storage.Open(dir, nil)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	l := logdserver.New(&logdserver.Spec{Storage: st})
	if err := l.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start logd: %v", err)
	}
	d := docdserver.New(&docdserver.Spec{LogdAddr: l.TCPAddr()})
	if err := d.StartClientTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start docd client face: %v", err)
	}
	if err := d.StartTCP("127.0.0.1:0"); err != nil {
		t.Fatalf("start docd mount face: %v", err)
	}
	tr := &tier{logd: l, docd: d}
	t.Cleanup(tr.stop)
	t.Cleanup(func() { st.Close() })
	return tr
}

func (tr *tier) stop() {
	tr.docd.StopClientTCP()
	tr.docd.StopTCP()
	tr.logd.StopTCP()
}

// writeAt attempts one write and reports what happened, without failing the test — the
// failures here are the subject rather than the setup.
func writeAt(s *LogdStore, id string) (time.Duration, error) {
	start := time.Now()
	_, _, err := s.Write(Write{
		At:    RefAt("demo", "pr", id).At(),
		Value: Obj{"stage": "open"}.Payload(),
	})
	return time.Since(start), err
}

// mustRecover polls a write until it lands, and says how long recovery took. A store that
// never recovers fails here, which is what the outage looked like from outside.
func mustRecover(t *testing.T, s *LogdStore, id string, within time.Duration) {
	t.Helper()
	start := time.Now()
	var last error
	for attempt := 1; time.Since(start) < within; attempt++ {
		if _, err := writeAt(s, fmt.Sprintf("%s-%d", id, attempt)); err == nil {
			t.Logf("recovered after %s (%d attempts)", time.Since(start).Round(time.Millisecond), attempt)
			return
		} else {
			last = err
		}
	}
	t.Fatalf("no write landed within %s of connectivity returning; last error: %v", within, last)
}

// The connection is CLOSED under a live store: the peer's FIN arrives, the read-pump sees
// it, and the pending request fails at once. Everything the store needs in order to know
// is delivered to it, so this is the easy half — and it runs in the default suite because
// it costs milliseconds.
func TestStoreRecoversFromClosedConnection(t *testing.T) {
	s, g := storeThroughGate(t)
	if _, err := writeAt(s, "before"); err != nil {
		t.Fatalf("write before the cut: %v", err)
	}

	g.cut()

	// The write across the cut may fail or may land on a connection re-established in
	// time; both are correct. What is not correct is failing forever.
	if d, err := writeAt(s, "during"); err != nil {
		t.Logf("write across the cut failed after %s: %v", d.Round(time.Millisecond), err)
	}

	mustRecover(t, s, "after", 20*time.Second)

	// A read too, since the evidence from the outage says the wedge was not write-specific:
	// /status failed rather than answering with a stale revision, and /status is a read.
	if _, ok, err := s.Get(RefAt("demo", "pr", "before")); err != nil || !ok {
		t.Fatalf("read after recovery: ok=%v err=%v — what was written before the cut is unreadable", ok, err)
	}
}

// The connection goes DARK: no FIN, no reset, nothing delivered, both sockets open. This is
// the outage's shape, and the only thing that can notice it is a timeout.
//
// It is long by construction — the noticing IS the runtime, and making it fast would test
// something else. What the timings say when it passes is worth as much as the pass: how
// long a viff spends unable to write before it repairs itself is a number an operator
// eventually asks for.
func TestStoreRecoversFromDarkConnection(t *testing.T) {
	requireLong(t)

	s, g := storeThroughGate(t)
	if _, err := writeAt(s, "before"); err != nil {
		t.Fatalf("write before the blackout: %v", err)
	}

	g.darken()

	// Several writers at once, because viffd had several — sources, the engine and the
	// mirror all write, and a session that serializes requests under one mutex can fail
	// differently for the second caller than for the first.
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := writeAt(s, fmt.Sprintf("during-%d", i))
			t.Logf("concurrent write %d into the dark: %s, err=%v", i, d.Round(time.Millisecond), err)
		}()
	}
	d, err := writeAt(s, "during")
	t.Logf("write into the dark: %s, err=%v", d.Round(time.Millisecond), err)
	if err == nil {
		t.Fatal("a write into a blackholed connection reported success — nothing could have received it")
	}
	wg.Wait()

	// Heal while the session still believes it is connected. This is the outage's shape and
	// the reason the test is worth its runtime: docd was reachable again at 14:26:14, and the
	// question is whether anything at this end ever finds that out. The route is fine; the
	// socket the store holds is not, and nothing is going to tell it. Only a timeout can.
	g.heal()
	mustRecover(t, s, "after", 60*time.Second)

	if _, ok, err := s.Get(RefAt("demo", "pr", "before")); err != nil || !ok {
		t.Fatalf("read after recovery: ok=%v err=%v — what was written before the blackout is unreadable", ok, err)
	}
}

// A WATCH is open while the outage happens, which is the one thing viffd has that the
// tests above do not. It is not a detail: a subtree watch re-establishes itself, and
// re-establishing means connecting, and connecting is done under the session's mutex.
//
//	pkg/entity/logdstore.go   ctx, cancel := context.WithCancel(s.ctx)   // no deadline
//	                      s.sess.Watch(ctx, path, …)
//	libctl/watch.go       s.mu.Lock(); ensureConnected(ctx)
//	libctl/logd.go        connectLocked: dial, back off, retry — until ctx is done
//
// The store's ctx ends when the store closes, so "until ctx is done" is "until the process
// exits". A watch that re-establishes into an outage therefore holds the session mutex for
// as long as the outage lasts, and every write waits on that mutex — not on its own
// deadline, since a Go mutex does not take a context. Each writer sits in the queue, is
// handed the lock when the outage ends, and finds the deadline it was given ten seconds ago
// already spent. What that produces, at every caller, is `context deadline exceeded`: the
// error the deployment produced, on writes that never reached the wire.
//
// So this test asserts what the other three cannot: that the store recovers WHILE SOMETHING
// IS WATCHING IT. The dark route is used because it is the case where the hold is longest —
// a dial into a blackhole completes, and it is the hello that then waits out the 30s wire
// timeout, mutex in hand.
func TestWatchReestablishDoesNotStarveWrites(t *testing.T) {
	requireLong(t)

	s, g := storeThroughGate(t)
	events, stop, err := s.WatchSubtree(3, "demo", "pr")
	if err != nil {
		t.Fatalf("watch subtree: %v", err)
	}
	defer stop()
	go func() {
		for range events { // a consumer that keeps up, so nothing here is about backpressure
		}
	}()

	if _, err := writeAt(s, "before"); err != nil {
		t.Fatalf("write before the blackout: %v", err)
	}

	g.darken()

	// Long enough for the session to notice (heartbeat: 10s interval, 5s timeout) and for
	// the watch to re-establish into the dark, which is the state under test. Waiting is
	// what the test is for; there is nothing to poll that would not be the assertion.
	time.Sleep(20 * time.Second)

	// The route is fine again from here. A store that recovers on its own has everything it
	// needs: a live peer, and its own retry.
	g.heal()
	mustRecover(t, s, "after", 20*time.Second)
}

// The peer is REPLACED, which is what a deployment does. The two tests above interrupt a
// connection to a server that stays where it was; here the server is stopped and a
// different process comes up over the same data, as `make deploy` rolls the docd pod.
//
// It is a distinct thing to test because the store's counterpart is now a stranger: a new
// serverID, no memory of this client, no memory of the ids on the session's outstanding
// requests. A reconnect that quietly assumed continuity would pass the tests above and fail
// here, and here is the case that actually happened.
//
// It runs in the default suite: the roll is scripted rather than waited for, so nothing in
// it costs a timeout.
func TestStoreRecoversFromReplacedPeer(t *testing.T) {
	dir := t.TempDir()
	old := startTier(t, dir)
	g := newGate(t, old.docd.ClientTCPAddr())
	s := storeVia(t, g)

	if _, err := writeAt(s, "before"); err != nil {
		t.Fatalf("write before the roll: %v", err)
	}

	// Down goes the pod, and with it every connection through the gate.
	old.stop()
	g.cut()
	if _, err := writeAt(s, "during"); err == nil {
		t.Fatal("a write with the store's peer stopped reported success")
	}

	// Up comes a new one over the same volume, at a new address, and the gate is the
	// Service that keeps the store's address the same across it.
	fresh := startTier(t, dir)
	g.point(fresh.docd.ClientTCPAddr())

	mustRecover(t, s, "after", 20*time.Second)

	// The same volume, so the roll must not have cost anything that was written before it.
	if _, ok, err := s.Get(RefAt("demo", "pr", "before")); err != nil || !ok {
		t.Fatalf("read after the roll: ok=%v err=%v — what was written before it is gone", ok, err)
	}
}
