package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SDK hosts such as T3 Code keep one Claude Code process per thread and talk
// to it in stream-json on stdin and stdout. Handing that process over with
// exec would leave the thread on one account for its whole life, so the
// launcher stays between them instead. When Claude reports that the account's
// limit rejected a request, the turn ends as usual; the launcher then
// replaces the process with one on the next account that resumes the same
// session, and replays the host's initialize and settings requests so the
// host does not notice. Nothing is resubmitted: the host's next message
// continues the conversation on the new account.

// streamJSON reports whether the host drives Claude over stream-json.
func streamJSON(args []string) bool {
	return flagValue(args, "--input-format") == "stream-json" && flagValue(args, "--output-format") == "stream-json"
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v
		}
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// sessionArgs keeps every flag but the ones that choose the session, and
// resumes the given session instead.
func sessionArgs(args []string, session string) []string {
	withValue := []string{"--session-id", "--resume-session-at", "--resume-drops-turn"}
	optionalValue := []string{"--resume", "-r"}
	bare := []string{"--continue", "-c", "--fork-session"}
	out := []string{}
	for i := 0; i < len(args); i++ {
		name, _, hasEq := strings.Cut(args[i], "=")
		switch {
		case slices.Contains(bare, args[i]):
		case slices.Contains(withValue, name):
			if !hasEq {
				i++
			}
		case slices.Contains(optionalValue, name):
			if !hasEq && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		default:
			out = append(out, args[i])
		}
	}
	return append(out, "--resume="+session)
}

// stateRequests are the host's control requests that configure a process;
// a replacement process receives them again. Later ones of the same kind
// replace earlier ones, except those that apply cumulatively.
var stateRequests = []string{"initialize", "set_permission_mode", "set_model", "set_max_thinking_tokens", "set_mcp_permission_mode_override", "set_cwd", "mcp_set_servers"}
var cumulativeRequests = []string{"apply_flag_settings", "mcp_toggle"}

type streamLine struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	SessionID string          `json:"session_id"`
	Request   json.RawMessage `json:"request"`
	Response  struct {
		RequestID string `json:"request_id"`
	} `json:"response"`
	RateLimit struct {
		Status   string `json:"status"`
		ResetsAt int64  `json:"resetsAt"`
	} `json:"rate_limit_info"`
}

// lineType reads a line's top-level type, parsing it only when one of the
// types of interest appears; Claude and the SDK write compact JSON.
func lineType(line []byte, types ...string) (streamLine, bool) {
	var l streamLine
	for _, t := range types {
		if bytes.Contains(line, []byte(`"type":"`+t+`"`)) {
			if json.Unmarshal(line, &l) == nil && l.Type == t {
				return l, true
			}
		}
	}
	return l, false
}

type stateRequest struct {
	subtype string
	request json.RawMessage
}

// nextProcess decides where a thread goes after its account hit its limit:
// the arguments and environment for the replacement, or false to stay.
type nextProcess func(session string, resetsAt *time.Time) (args, env []string, ok bool)

type streamProxy struct {
	claude string
	host   io.Reader
	out    io.Writer
	next   nextProcess

	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser // nil while the process is being replaced
	draining  io.WriteCloser // a process on hold or being replaced; it gets only answers to its requests
	pending   map[string]bool
	queue     [][]byte
	state     []stateRequest
	replayed  map[string]bool
	replays   int
	hostEnded bool
}

func superviseStream(claude string, args []string, ch choice) int {
	p := &streamProxy{claude: claude, host: os.Stdin, out: os.Stdout}
	ctx := context.Background()
	p.next = func(session string, resetsAt *time.Time) ([]string, []string, bool) {
		if ch.account == nil {
			return nil, nil, false
		}
		reportLimited(ctx, ch.account.ID, resetsAt)
		next, err := pick(ctx, map[string]bool{ch.account.ID: true})
		if err != nil || next.account == nil {
			fmt.Fprintf(os.Stderr, "nebula: %s reached its usage limit and no other connected account has room.\n", ch.label())
			return nil, nil, false
		}
		fmt.Fprintf(os.Stderr, "nebula: %s reached its usage limit; continuing this session on %s\n", ch.label(), next.label())
		_ = os.WriteFile(lastChoiceFile(), []byte(next.profile.Dir+"\n"), 0600)
		if managed, _ := managedDir(); filepath.Dir(next.profile.Dir) == managed {
			_ = linkShared(next.profile.Dir)
		}
		ch = next
		return sessionArgs(args, session), streamEnv(ch), true
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		for s := range sigs {
			p.signal(s)
		}
	}()
	return p.run(args, streamEnv(ch))
}

func streamEnv(ch choice) []string {
	return append(ch.env(""), "NEBULA_STREAM=1")
}

func (p *streamProxy) signal(s os.Signal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		if p.cmd.Process.Signal(s) != nil {
			_ = p.cmd.Process.Kill()
		}
	}
}

// run starts Claude and replaces it on every account switch until the host
// closes stdin and Claude exits; it returns Claude's exit code.
func (p *streamProxy) run(args, env []string) int {
	p.pending, p.replayed = map[string]bool{}, map[string]bool{}
	go p.readHost()
	for first := true; ; first = false {
		cmd := exec.Command(p.claude, args...)
		cmd.Env, cmd.Stderr = env, os.Stderr
		stdin, err := cmd.StdinPipe()
		if err != nil {
			fmt.Fprintln(os.Stderr, "nebula:", err)
			return 1
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			fmt.Fprintln(os.Stderr, "nebula:", err)
			return 1
		}
		if err = cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "nebula:", err)
			return 1
		}
		p.attach(cmd, newQueuedWriter(stdin), !first)
		nextArgs, nextEnv, switching := p.pump(stdout)
		err = cmd.Wait()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				fmt.Fprintln(os.Stderr, "nebula:", err)
				return 1
			}
			code = exit.ExitCode()
		}
		p.mu.Lock()
		for _, w := range []io.WriteCloser{p.draining, p.stdin} {
			if w != nil {
				_ = w.Close()
			}
		}
		p.draining, p.stdin, p.cmd = nil, nil, nil
		p.mu.Unlock()
		if !switching {
			return code
		}
		args, env = nextArgs, nextEnv
	}
}

// queuedWriter writes to a process's stdin from its own goroutine. A pipe
// holds little (4 KiB on Windows), and Claude reads its input only while it
// is not itself blocked writing output, so writes must never wait while the
// proxy holds its lock or before it reads Claude's output.
type queuedWriter struct {
	mu     sync.Mutex
	ready  *sync.Cond
	lines  [][]byte
	closed bool
}

func newQueuedWriter(w io.WriteCloser) *queuedWriter {
	q := &queuedWriter{}
	q.ready = sync.NewCond(&q.mu)
	go func() {
		for {
			q.mu.Lock()
			for len(q.lines) == 0 && !q.closed {
				q.ready.Wait()
			}
			lines, closed := q.lines, q.closed
			q.lines = nil
			q.mu.Unlock()
			for _, line := range lines {
				_, _ = w.Write(line) // after the process is gone, lines are dropped
			}
			if closed && len(lines) == 0 {
				_ = w.Close()
				return
			}
		}
	}()
	return q
}

func (q *queuedWriter) Write(b []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return 0, io.ErrClosedPipe
	}
	q.lines = append(q.lines, slices.Clone(b))
	q.ready.Signal()
	return len(b), nil
}

// Close closes the stdin once everything queued before it is written.
func (q *queuedWriter) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.ready.Signal()
	return nil
}

// attach makes a started process the one the host talks to. A replacement
// first receives the host's configuration, then whatever the host sent while
// the old process ended.
func (p *streamProxy) attach(cmd *exec.Cmd, stdin io.WriteCloser, replay bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cmd, p.pending = cmd, map[string]bool{}
	if replay {
		for _, s := range p.state {
			p.replays++
			id := fmt.Sprintf("nebula-replay-%d", p.replays)
			line, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": id, "request": s.request})
			p.replayed[id] = true
			_, _ = stdin.Write(append(line, '\n'))
		}
	}
	for _, line := range p.queue {
		_, _ = stdin.Write(line)
	}
	p.queue = nil
	if p.hostEnded {
		_ = stdin.Close()
		return
	}
	p.stdin = stdin
}

// readHost forwards the host's lines to the current process, remembering
// configuration requests for a replacement.
func (p *streamProxy) readHost() {
	r := bufio.NewReaderSize(p.host, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			p.fromHost(line)
		}
		if err != nil {
			break
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hostEnded = true
	for _, w := range []io.WriteCloser{p.stdin, p.draining} {
		if w != nil {
			_ = w.Close()
		}
	}
	p.stdin, p.draining = nil, nil
}

func (p *streamProxy) fromHost(line []byte) {
	l, _ := lineType(line, "control_request", "control_response")
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.Type == "control_request" {
		var req struct {
			Subtype string `json:"subtype"`
		}
		_ = json.Unmarshal(l.Request, &req)
		if slices.Contains(stateRequests, req.Subtype) {
			p.state = slices.DeleteFunc(p.state, func(s stateRequest) bool { return s.subtype == req.Subtype })
		}
		if slices.Contains(stateRequests, req.Subtype) || slices.Contains(cumulativeRequests, req.Subtype) {
			p.state = append(p.state, stateRequest{req.Subtype, slices.Clone(l.Request)})
		}
	}
	if l.Type == "control_response" && p.pending[l.Response.RequestID] {
		// An answer to the process being replaced, which may be waiting on it.
		delete(p.pending, l.Response.RequestID)
		if p.draining != nil {
			_, _ = p.draining.Write(line)
			if len(p.pending) == 0 {
				_ = p.draining.Close()
				p.draining = nil
			}
			return
		}
	}
	if p.stdin == nil {
		p.queue = append(p.queue, slices.Clone(line))
		return
	}
	_, _ = p.stdin.Write(line)
}

// pump forwards Claude's lines to the host until Claude exits. When a
// request was rejected at the account's limit, it decides at the end of
// that turn whether to replace the process, then lets the old one finish.
func (p *streamProxy) pump(stdout io.Reader) (nextArgs, nextEnv []string, switching bool) {
	r := bufio.NewReaderSize(stdout, 1<<16)
	var limitedUntil *time.Time
	limited := false
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			l, _ := lineType(line, "rate_limit_event", "result", "control_request", "control_response")
			forward := true
			switch l.Type {
			case "rate_limit_event":
				if l.RateLimit.Status == "rejected" {
					limited = true
					if l.RateLimit.ResetsAt > 0 {
						t := time.Unix(l.RateLimit.ResetsAt, 0)
						limitedUntil = &t
					}
				}
			case "control_request":
				p.mu.Lock()
				p.pending[l.RequestID] = true
				p.mu.Unlock()
			case "control_response":
				p.mu.Lock()
				if p.replayed[l.Response.RequestID] {
					delete(p.replayed, l.Response.RequestID)
					forward = false // the host already has its answer
				}
				p.mu.Unlock()
			}
			decide := l.Type == "result" && limited && !switching && l.SessionID != ""
			if decide {
				// Before the host sees the turn end and sends its next message.
				p.hold()
			}
			if forward {
				_, _ = p.out.Write(line)
			}
			if decide {
				limited = false
				if nextArgs, nextEnv, switching = p.next(l.SessionID, limitedUntil); switching {
					p.retire()
				} else {
					p.release()
				}
			}
		}
		if err != nil {
			return nextArgs, nextEnv, switching
		}
	}
}

// hold stops sending the host's messages to the current process while the
// launcher decides whether to replace it; they wait in the queue.
func (p *streamProxy) hold() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.draining, p.stdin = p.stdin, nil
}

// release returns a held process to the host, with what it sent meanwhile.
func (p *streamProxy) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.draining == nil {
		return
	}
	for _, line := range p.queue {
		_, _ = p.draining.Write(line)
	}
	p.queue = nil
	p.stdin, p.draining = p.draining, nil
}

// retire lets a held process finish: it still receives answers to its open
// requests, then sees end of input and exits; one that does not is killed.
// The queue waits for its replacement.
func (p *streamProxy) retire() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.draining != nil && len(p.pending) == 0 {
		_ = p.draining.Close()
		p.draining = nil
	}
	cmd := p.cmd
	time.AfterFunc(15*time.Second, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.cmd == cmd {
			_ = cmd.Process.Kill()
		}
	})
}
