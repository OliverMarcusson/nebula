package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestFakeStreamClaude is not a test: run as a child process, it plays a
// stream-json Claude Code signed in to FAKE_ACCOUNT, whose limit any message
// containing "hit limit" reaches.
func TestFakeStreamClaude(t *testing.T) {
	account := os.Getenv("FAKE_ACCOUNT")
	if account == "" {
		t.Skip("helper process")
	}
	out := json.NewEncoder(os.Stdout)
	r := bufio.NewReader(os.Stdin)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var l struct {
				Type      string `json:"type"`
				RequestID string `json:"request_id"`
				Request   struct {
					Subtype string `json:"subtype"`
				} `json:"request"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}
			_ = json.Unmarshal(line, &l)
			switch {
			case l.Type == "control_request":
				_ = out.Encode(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": l.RequestID, "response": map[string]string{"account": account, "handled": l.Request.Subtype}}})
			case l.Type == "user" && strings.Contains(l.Message.Content, "hit limit"):
				_ = out.Encode(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "rejected", "resetsAt": 1791471600}})
				_ = out.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": true, "session_id": "sess-1"})
			case l.Type == "user":
				_ = out.Encode(map[string]any{"type": "result", "subtype": "success", "result": account + " " + strings.Join(os.Args[2:], " "), "session_id": "sess-1"})
			}
		}
		if err != nil {
			os.Exit(0)
		}
	}
}

type streamHost struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Reader
}

func (h *streamHost) send(v any) {
	b, _ := json.Marshal(v)
	if _, err := h.in.Write(append(b, '\n')); err != nil {
		h.t.Fatal(err)
	}
}

func (h *streamHost) read() map[string]any {
	h.t.Helper()
	got := make(chan map[string]any, 1)
	go func() {
		line, _ := h.out.ReadBytes('\n')
		var m map[string]any
		_ = json.Unmarshal(line, &m)
		got <- m
	}()
	select {
	case m := <-got:
		return m
	case <-time.After(10 * time.Second):
		h.t.Fatal("timed out reading from the launcher")
		return nil
	}
}

func startStream(t *testing.T, next nextProcess) (*streamHost, chan int) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &streamProxy{claude: os.Args[0], host: inR, out: outW, next: next}
	args := []string{"-test.run=^TestFakeStreamClaude$", "--", "--output-format", "stream-json", "--input-format", "stream-json", "--session-id=sess-1", "--model", "opus"}
	done := make(chan int, 1)
	go func() {
		done <- p.run(args, append(os.Environ(), "FAKE_ACCOUNT=first"))
		outW.Close()
	}()
	return &streamHost{t: t, in: inW, out: bufio.NewReader(outR)}, done
}

func TestStreamSwitchesAccountAfterLimit(t *testing.T) {
	var gotSession string
	var gotResets *time.Time
	h, done := startStream(t, func(session string, resetsAt *time.Time) ([]string, []string, bool) {
		gotSession, gotResets = session, resetsAt
		args := sessionArgs([]string{"-test.run=^TestFakeStreamClaude$", "--", "--output-format", "stream-json", "--input-format", "stream-json", "--session-id=sess-1", "--model", "opus"}, session)
		return args, append(os.Environ(), "FAKE_ACCOUNT=second"), true
	})
	h.send(map[string]any{"type": "control_request", "request_id": "r1", "request": map[string]any{"subtype": "initialize", "hooks": map[string]any{}}})
	if m := h.read(); m["type"] != "control_response" {
		t.Fatalf("initialize answered with %v", m)
	}
	h.send(map[string]any{"type": "control_request", "request_id": "r2", "request": map[string]any{"subtype": "set_model", "model": "sonnet"}})
	h.read()
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hit limit"}})
	if m := h.read(); m["type"] != "rate_limit_event" {
		t.Fatalf("expected the rate limit event, got %v", m)
	}
	if m := h.read(); m["type"] != "result" {
		t.Fatalf("expected the failed turn's result, got %v", m)
	}
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "continue"}})
	m := h.read()
	if m["type"] != "result" {
		t.Fatalf("expected the next turn's result (replayed answers must not reach the host), got %v", m)
	}
	result, _ := m["result"].(string)
	fields := strings.Fields(result)
	if fields[0] != "second" {
		t.Fatalf("the next turn ran on %q, want the second account", fields[0])
	}
	if !slices.Contains(fields, "--resume=sess-1") || slices.Contains(fields, "--session-id=sess-1") || !slices.Contains(fields, "opus") {
		t.Fatalf("replacement arguments: %v", fields)
	}
	if gotSession != "sess-1" || gotResets == nil || gotResets.Unix() != 1791471600 {
		t.Fatalf("switch decided with session %q, resets %v", gotSession, gotResets)
	}
	h.in.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("launcher did not exit after the host closed stdin")
	}
}

func TestStreamStaysWithoutAnotherAccount(t *testing.T) {
	h, done := startStream(t, func(string, *time.Time) ([]string, []string, bool) { return nil, nil, false })
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hit limit"}})
	h.read()
	h.read()
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "continue"}})
	if m := h.read(); !strings.HasPrefix(fmt.Sprint(m["result"]), "first ") {
		t.Fatalf("expected the same process to answer, got %v", m)
	}
	h.in.Close()
	<-done
}

func TestSessionArgs(t *testing.T) {
	cases := map[string][]string{
		"--model opus --session-id=a --resume=sess":                 {"--model", "opus", "--resume=sess"},
		"--resume b --fork-session --verbose --resume=sess":         {"--verbose", "--resume=sess"},
		"-c --resume --model x --resume-session-at=u --resume=sess": {"--model", "x", "--resume=sess"},
	}
	for in, want := range cases {
		args := strings.Fields(in)
		if got := sessionArgs(args[:len(args)-1], "sess"); !slices.Equal(got, want) {
			t.Errorf("sessionArgs(%q) = %v, want %v", in, got, want)
		}
	}
	if !streamJSON([]string{"--output-format", "stream-json", "--verbose", "--input-format=stream-json"}) || streamJSON([]string{"--output-format", "stream-json", "-p", "hi"}) {
		t.Error("streamJSON misread its flags")
	}
}
