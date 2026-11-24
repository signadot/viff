package delivery

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"go.viff.dev/viff/pkg/codec"
)

// Client is a task process's end of the channel: it asks, and the runner, which holds the
// credential, answers with what happened. What it may ask is the protocol's ops; whether the
// runner does it is the runner's to decide.
//
// Several may ask at once. Each request is named (Request.ID) and each answer names its request,
// so an answer that comes ahead of an earlier request's reaches the one that asked.
type Client struct {
	conn    net.Conn
	br      *bufio.Reader
	rights  *rights    // the descriptors frames bring, where the connection carries them (a unix socket)
	writing sync.Mutex // one frame onto the connection at a time

	mu      sync.Mutex
	last    int
	waiting map[int]*asker
	ended   error // why no answer will come, once the channel has ended
}

// asker is one request waiting for its answer: where it hears, and whether a stream frame has
// come for it, since one at most does.
type asker struct {
	got      chan heard
	streamed bool
}

// heard is what one Ask hears: the runner's answer, with the descriptors a stream frame
// brought, or why there is none.
type heard struct {
	rep   Reply
	files []*os.File
	err   error
}

// Attach opens the task process's end of the channel, inherited as descriptor fd.
func Attach(fd uintptr) (*Client, error) {
	f := os.NewFile(fd, "deliver")
	conn, err := net.FileConn(f)
	f.Close() // FileConn dups; the connection is the end now
	if err != nil {
		return nil, fmt.Errorf("the delivery channel on fd %d: %w", fd, err)
	}
	return NewClient(conn), nil
}

// NewClient is a Client over a connection already open. Over a unix socket the frames are read
// with the descriptors they carry (rights); over anything else no frame carries one.
func NewClient(conn net.Conn) *Client {
	c := &Client{conn: conn, waiting: map[int]*asker{}}
	var r io.Reader = conn
	if uc, ok := conn.(*net.UnixConn); ok {
		c.rights = newRights(uc)
		r = c.rights
	}
	c.br = bufio.NewReader(r)
	go c.hear()
	return c
}

// Ask sends one request and waits for the runner's answer to it. A stream frame ahead of the
// answer brings descriptors nobody asked to be handed, and they are closed.
func (c *Client) Ask(req Request) (Reply, error) { return c.AskStreaming(req, nil) }

// AskStreaming sends one request and waits for the runner's answer, handing stream the
// descriptors a stream frame brings ahead of it (Reply.Streams): the task's ends of what the act
// streams, which are the caller's to close. One stream frame at most comes before the answer.
func (c *Client) AskStreaming(req Request, stream func([]*os.File)) (Reply, error) {
	c.mu.Lock()
	if c.ended != nil {
		c.mu.Unlock()
		return Reply{}, fmt.Errorf("the runner's answer: %w", c.ended)
	}
	c.last++
	req.ID = c.last
	// Room for the one stream frame and the answer, or the end of the channel, so hear and tell
	// never wait on an asker: a second stream frame for one request does not read (hear).
	got := make(chan heard, 2)
	c.waiting[req.ID] = &asker{got: got}
	c.mu.Unlock()
	if err := c.send(req); err != nil {
		c.mu.Lock()
		delete(c.waiting, req.ID)
		c.mu.Unlock()
		return Reply{}, err
	}
	for {
		h := <-got
		if h.err != nil {
			return Reply{}, fmt.Errorf("the runner's answer: %w", h.err)
		}
		if h.rep.Streams > 0 {
			if stream == nil {
				closeAll(h.files)
			} else {
				stream(h.files)
			}
			continue
		}
		return h.rep, nil
	}
}

func (c *Client) send(req Request) error {
	body, err := codec.Marshal(req, codec.Tony)
	if err != nil {
		return err
	}
	// Refused before it is sent: the runner would drop it, and the task learns why here.
	if len(body) > maxFrame {
		return fmt.Errorf("the request is %w; send less", frameTooLarge{len(body)})
	}
	c.writing.Lock()
	defer c.writing.Unlock()
	return writeFrame(c.conn, body)
}

// hear reads the runner's answers, each to the Ask it names, until the channel ends. An answer
// that names no request is to a frame the runner read no request in, which Ask does not send,
// and is dropped.
//
// A FRAME THAT DOES NOT READ AS AN ANSWER ENDS THE CHANNEL. It could be the answer to any
// request in flight, so none of them can be told what became of it, and the ones still being
// performed would be answered to nobody. Everyone waiting hears that what they asked may have
// been done, which is what keeps a write from being made twice on a retry.
func (c *Client) hear() {
	// However the reading ends, this end is closed, so the runner sees it go: it then gives up
	// what waits its turn, and does not write answers nobody reads. A descriptor read and
	// handed to nobody is closed with it.
	defer c.conn.Close()
	if c.rights != nil {
		defer c.rights.drop()
	}
	for {
		var start int64
		if c.rights != nil {
			start = c.rights.consumed(c.br)
		}
		raw, err := readFrame(c.br)
		var big frameTooLarge
		if err != nil && !errors.As(err, &big) {
			c.tell(heard{err: err}) // the runner's end is gone
			return
		}
		var rep Reply
		if err == nil {
			err = codec.Unmarshal(raw, &rep, codec.Tony)
		}
		// A STREAM FRAME TAKES THE DESCRIPTORS SENT WITH IT, which are queued by the time it
		// decodes. A frame does not read where it says it carries some and the channel read
		// another count, or none at all; where descriptors came with a frame already read whole
		// that took none (unclaimed); or where it is a second stream frame for one request, which
		// an asker has no room for. In each the descriptors are some other frame's, or nobody's.
		var files []*os.File
		if err == nil && c.rights != nil && c.rights.unclaimed(start) {
			err = errors.New("descriptors came on the channel with a frame that takes none")
		}
		if err == nil && rep.Streams > 0 {
			if c.rights == nil {
				err = fmt.Errorf("a frame says it carries %d descriptors, and this connection is no unix socket, which carries none", rep.Streams)
			} else {
				files, err = c.rights.take(rep.Streams)
			}
		}
		if err == nil && rep.Streams > 0 {
			c.mu.Lock()
			if a, ok := c.waiting[rep.ID]; ok && a.streamed {
				err = fmt.Errorf("a second stream frame for request %d, which has one at most", rep.ID)
			}
			c.mu.Unlock()
		}
		if err != nil {
			closeAll(files)
			c.tell(heard{err: fmt.Errorf("an answer on the channel does not read (%w), so the channel is closed and "+
				"whether what was asked was done is not known; read it back before doing it again", err)})
			return
		}
		c.mu.Lock()
		a, ok := c.waiting[rep.ID]
		if rep.Streams == 0 {
			delete(c.waiting, rep.ID) // the answer; a stream frame leaves the asker waiting for it
		} else if ok {
			a.streamed = true
		}
		c.mu.Unlock()
		if ok {
			a.got <- heard{rep: rep, files: files}
		} else {
			closeAll(files)
		}
	}
}

// tell ends the channel for everyone: those waiting hear h, and whoever asks next hears the
// same.
func (c *Client) tell(h heard) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ended = h.err
	for id, a := range c.waiting {
		a.got <- h
		delete(c.waiting, id)
	}
}

// Close closes the task's end.
func (c *Client) Close() error { return c.conn.Close() }
