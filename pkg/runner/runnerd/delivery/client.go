package delivery

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/signadot/verse/pkg/codec"
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
	writing sync.Mutex // one frame onto the connection at a time

	mu      sync.Mutex
	last    int
	waiting map[int]chan heard
	ended   error // why no answer will come, once the channel has ended
}

// heard is what one Ask hears: the runner's answer, or why there is none.
type heard struct {
	rep Reply
	err error
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

// NewClient is a Client over a connection already open.
func NewClient(conn net.Conn) *Client {
	c := &Client{conn: conn, br: bufio.NewReader(conn), waiting: map[int]chan heard{}}
	go c.hear()
	return c
}

// Ask sends one request and waits for the runner's answer to it.
func (c *Client) Ask(req Request) (Reply, error) {
	c.mu.Lock()
	if c.ended != nil {
		c.mu.Unlock()
		return Reply{}, fmt.Errorf("the runner's answer: %w", c.ended)
	}
	c.last++
	req.ID = c.last
	got := make(chan heard, 1)
	c.waiting[req.ID] = got
	c.mu.Unlock()
	if err := c.send(req); err != nil {
		c.mu.Lock()
		delete(c.waiting, req.ID)
		c.mu.Unlock()
		return Reply{}, err
	}
	h := <-got
	if h.err != nil {
		return Reply{}, fmt.Errorf("the runner's answer: %w", h.err)
	}
	return h.rep, nil
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
	// what waits its turn, and does not write answers nobody reads.
	defer c.conn.Close()
	for {
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
		if err != nil {
			c.tell(heard{err: fmt.Errorf("an answer on the channel does not read (%w), so the channel is closed and "+
				"whether what was asked was done is not known; read it back before doing it again", err)})
			return
		}
		c.mu.Lock()
		got, ok := c.waiting[rep.ID]
		delete(c.waiting, rep.ID)
		c.mu.Unlock()
		if ok {
			got <- heard{rep: rep}
		}
	}
}

// tell ends the channel for everyone: those waiting hear h, and whoever asks next hears the
// same.
func (c *Client) tell(h heard) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ended = h.err
	for id, got := range c.waiting {
		got <- h
		delete(c.waiting, id)
	}
}

// Close closes the task's end.
func (c *Client) Close() error { return c.conn.Close() }
