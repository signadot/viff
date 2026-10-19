package delivery

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/signadot/verse/codec"
)

// Client is a task process's end of the channel: it asks, and the runner, which holds the
// credential, answers with what happened. What it may ask is the protocol's three ops; whether
// the runner does it is the runner's to decide.
type Client struct {
	mu   sync.Mutex // one request and its reply at a time
	conn net.Conn
	br   *bufio.Reader
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
	return &Client{conn: conn, br: bufio.NewReader(conn)}
}

// Ask sends one request and reads the runner's reply.
func (c *Client) Ask(req Request) (Reply, error) {
	body, err := codec.Marshal(req, codec.Tony)
	if err != nil {
		return Reply{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := writeFrame(c.conn, body); err != nil {
		return Reply{}, err
	}
	raw, err := readFrame(c.br)
	if err != nil {
		return Reply{}, err
	}
	var rep Reply
	if err := codec.Unmarshal(raw, &rep, codec.Tony); err != nil {
		return Reply{}, err
	}
	return rep, nil
}

// Close closes the task's end.
func (c *Client) Close() error { return c.conn.Close() }
