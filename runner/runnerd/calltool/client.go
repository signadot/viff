package calltool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/signadot/verse/codec"
)

// Client is a program's end: the socket the task process answers on.
type Client struct {
	hc *http.Client
}

// Dial is a client of the server at path. Nothing is connected until a call is made.
func Dial(path string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
	// No timeout, deliberately: an `ask` waits for a person, and the caller's own context — a
	// shell's ctrl-C — is what ends the wait.
	return &Client{hc: &http.Client{Transport: tr}}
}

// Tools is what the server offers.
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	if err := c.do(ctx, http.MethodGet, "/tools", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Call performs one call. A refusal is a Result; a failure, including a tool the server has
// not got, is an error in the server's words.
func (c *Client) Call(ctx context.Context, tool string, input map[string]any) (Result, error) {
	var res Result
	err := c.do(ctx, http.MethodPost, "/call", Call{Tool: tool, Input: input}, &res)
	return res, err
}

// The host in the URL is a name for the socket, not an address: the transport dials the path
// whatever it says.
const base = "http://verse-call-tool"

func (c *Client) do(ctx context.Context, method, route string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := codec.Marshal(body, codec.Tony)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+route, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", codec.MediaType(codec.Tony))
	}
	req.Header.Set("Accept", codec.MediaType(codec.Tony))
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("reaching the task's tools: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		// The body is the message, in words (codec.HTTPError); the status stands in when there
		// are none.
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("%s", msg)
	}
	f, ok := codec.FormatForMedia(resp.Header.Get("Content-Type"))
	if !ok {
		f = codec.Tony
	}
	return codec.Unmarshal(data, out, f)
}
