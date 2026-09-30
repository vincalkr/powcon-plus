// Package ctl is the local control channel between the CLI, the window and
// the daemon. Protocol: one JSON line per request, one per response (or a
// stream of JSON lines for subscriptions).
package ctl

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Request is a call aimed at the daemon.
type Request struct {
	Cmd  string         `json:"cmd"`
	Args map[string]any `json:"args,omitempty"`
}

// Response is the daemon's reply. Data is omitted for simple commands.
type Response struct {
	OK   bool            `json:"ok"`
	Err  string          `json:"err,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// StreamHandler handles a connection in streaming mode.
type StreamHandler func(args map[string]any, enc *json.Encoder, deadline time.Duration) error

// Server listens on a Unix socket in the data dir.
type Server struct {
	path     string
	ln       net.Listener
	commands map[string]func(args map[string]any) (any, error)
	streams  map[string]StreamHandler
}

// NewServer creates the socket (removing a stale one when no daemon is running).
func NewServer(path string) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		// Check that nobody is already listening.
		c, derr := net.DialTimeout("unix", path, 300*time.Millisecond)
		if derr == nil {
			c.Close()
			return nil, fmt.Errorf("socket %s già in uso", path)
		}
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return &Server{
		path:     path,
		ln:       ln,
		commands: map[string]func(map[string]any) (any, error){},
		streams:  map[string]StreamHandler{},
	}, nil
}

// Path returns the socket path.
func (s *Server) Path() string { return s.path }

// Handle registers a request/response command.
func (s *Server) Handle(cmd string, fn func(args map[string]any) (any, error)) {
	s.commands[cmd] = fn
}

// HandleStream registers a streaming command.
func (s *Server) HandleStream(cmd string, fn StreamHandler) {
	s.streams[cmd] = fn
}

// Serve accepts connections until the listener is closed.
func (s *Server) Serve() error {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.serveConn(c)
	}
}

// Close closes the listener and removes the socket.
func (s *Server) Close() error {
	if s.ln != nil {
		s.ln.Close()
	}
	return os.Remove(s.path)
}

func (s *Server) serveConn(c net.Conn) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			writeResp(c, Response{OK: false, Err: "richiesta non valida: " + err.Error()})
			continue
		}
		if h, ok := s.streams[req.Cmd]; ok {
			enc := json.NewEncoder(c)
			deadline := time.Duration(0)
			if v, ok := req.Args["for_seconds"].(float64); ok && v > 0 {
				deadline = time.Duration(v) * time.Second
			}
			if err := h(req.Args, enc, deadline); err != nil {
				enc.Encode(Response{OK: false, Err: err.Error()})
			}
			return
		}
		h, ok := s.commands[req.Cmd]
		if !ok {
			writeResp(c, Response{OK: false, Err: "comando sconosciuto: " + req.Cmd})
			continue
		}
		data, err := h(req.Args)
		if err != nil {
			writeResp(c, Response{OK: false, Err: err.Error()})
			continue
		}
		raw, err := json.Marshal(data)
		if err != nil {
			writeResp(c, Response{OK: false, Err: "serializzazione: " + err.Error()})
			continue
		}
		writeResp(c, Response{OK: true, Data: raw})
	}
}

func writeResp(c net.Conn, r Response) {
	b, _ := json.Marshal(r)
	c.Write(append(b, '\n'))
}

// Call sends a request and waits for the reply.
func Call(path string, req Request) (Response, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return Response{}, fmt.Errorf("daemon non in esecuzione (%s): %w", path, err)
	}
	defer c.Close()
	b, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	if _, err := c.Write(append(b, '\n')); err != nil {
		return Response{}, err
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	sc := bufio.NewScanner(c)
	if !sc.Scan() {
		return Response{}, errors.New("nessuna risposta dal daemon")
	}
	var resp Response
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// Subscribe opens a streaming connection; consuming the reader is up to the caller.
func Subscribe(path string, req Request) (net.Conn, *bufio.Reader, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("daemon non in esecuzione (%s): %w", path, err)
	}
	b, err := json.Marshal(req)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	if _, err := c.Write(append(b, '\n')); err != nil {
		c.Close()
		return nil, nil, err
	}
	return c, bufio.NewReader(c), nil
}
