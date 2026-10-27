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
// stream-json Claude Code signed in to FAKE_ACCOUNT. The first account's
// limit is reached by "hit limit"; FAKE_ALWAYS_LIMIT exhausts replacements.
func TestFakeStreamClaude(t *testing.T) {
	account := os.Getenv("FAKE_ACCOUNT")
	if account == "" {
		t.Skip("helper process")
	}
	out := json.NewEncoder(os.Stdout)
	if os.Getenv("FAKE_NOISY") != "" {
		// Like Claude Code's startup hooks: more output than a pipe holds,
		// written before reading any input.
		for range 64 {
			_ = out.Encode(map[string]string{"type": "system", "subtype": "hook_response", "output": strings.Repeat("x", 4096)})
		}
	}
	r := bufio.NewReader(os.Stdin)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var l struct {
				Type      string `json:"type"`
				RequestID string `json:"request_id"`
				UUID      string `json:"uuid"`
				Request   struct {
					Subtype string `json:"subtype"`
				} `json:"request"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}
			_ = json.Unmarshal(line, &l)
			if account == "first" && l.Message.Content == "agents hit limit" {
				// Two background agents; the limit ends one before the main turn.
				_ = out.Encode(map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": []map[string]string{{"task_id": "a1", "task_type": "local_agent"}, {"task_id": "a2", "task_type": "local_agent"}, {"task_id": "b1", "task_type": "local_bash"}}})
				_ = out.Encode(map[string]any{"type": "system", "subtype": "task_updated", "task_id": "a1", "patch": map[string]string{"status": "failed", "error": "Agent terminated early due to an API error: You've hit your session limit"}})
				_ = out.Encode(map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": []map[string]string{{"task_id": "a2", "task_type": "local_agent"}}})
			}
			switch {
			case l.Type == "control_request":
				_ = out.Encode(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": l.RequestID, "response": map[string]string{"account": account, "handled": l.Request.Subtype}}})
			case l.Type == "user" && (account == "first" && strings.Contains(l.Message.Content, "hit limit") || os.Getenv("FAKE_ALWAYS_LIMIT") != ""):
				_ = out.Encode(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": "work already completed"}})
				_ = out.Encode(map[string]any{"type": "assistant", "error": "rate_limit", "message": map[string]any{"role": "assistant", "content": "Usage limit reached"}})
				_ = out.Encode(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "rejected", "resetsAt": 1791471600}})
				_ = out.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": true, "session_id": "sess-1"})
			case l.Type == "user" && l.Message.Content == "throttle":
				_ = out.Encode(map[string]any{"type": "assistant", "error": "rate_limit"})
				_ = out.Encode(map[string]any{"type": "result", "is_error": true, "session_id": "sess-1"})
			case l.Type == "user" && (account == "first" && strings.HasPrefix(l.Message.Content, "access denied") || os.Getenv("FAKE_ALWAYS_ACCESS_DENIED") != ""):
				denied := "Your organization has disabled Claude subscription access for Claude Code"
				_ = out.Encode(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": "work already completed"}})
				if l.Message.Content != "access denied result" {
					_ = out.Encode(map[string]any{"type": "assistant", "error": "authentication_failed", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": denied}}}})
				}
				_ = out.Encode(map[string]any{"type": "result", "is_error": true, "errors": []string{denied}, "session_id": "sess-1"})
			case l.Type == "user" && account == "first" && strings.HasPrefix(l.Message.Content, "subscription unavailable"):
				denied := "Claude Code can't use your Claude subscription, usually because a payment is past due"
				_ = out.Encode(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": "work already completed"}})
				if l.Message.Content != "subscription unavailable result" {
					_ = out.Encode(map[string]any{"type": "assistant", "error": "oauth_org_not_allowed", "api_error_status": 403, "api_error_code": "oauth_not_allowed_for_organization", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": denied}}}})
				}
				_ = out.Encode(map[string]any{"type": "result", "is_error": true, "api_error_status": 403, "api_error_code": "oauth_not_allowed_for_organization", "result": denied, "session_id": "sess-1"})
			case l.Type == "user" && l.Message.Content == "permission error":
				_ = out.Encode(map[string]any{"type": "assistant", "error": "permission_denied", "message": map[string]any{"role": "assistant", "content": "API Error: 403 forbidden"}})
				_ = out.Encode(map[string]any{"type": "result", "is_error": true, "session_id": "sess-1"})
			case l.Type == "user" && l.Message.Content == "quoted denial":
				_ = out.Encode(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": "Your organization has disabled Claude subscription access for Claude Code"}})
				_ = out.Encode(map[string]any{"type": "result", "is_error": false, "session_id": "sess-1"})
			case l.Type == "user":
				if l.UUID != "" {
					_ = out.Encode(map[string]any{"type": "user", "uuid": l.UUID, "message": map[string]any{"role": "user", "content": l.Message.Content}})
				}
				_ = out.Encode(map[string]any{"type": "result", "subtype": "success", "result": account + " " + strings.Join(os.Args[2:], " "), "prompt": l.Message.Content, "session_id": "sess-1"})
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

func startStream(t *testing.T, next nextProcess, env ...string) (*streamHost, chan int) {
	return startStreamWith(t, next, nil, env...)
}

// startStreamWith has prelude already queued from the host when Claude
// starts, as when T3 Code writes its first messages straight away.
func startStreamWith(t *testing.T, next nextProcess, prelude []byte, env ...string) (*streamHost, chan int) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &streamProxy{claude: os.Args[0], host: inR, out: outW, next: next}
	if prelude != nil {
		p.queue = [][]byte{prelude}
	}
	args := []string{"-test.run=^TestFakeStreamClaude$", "--", "--output-format", "stream-json", "--input-format", "stream-json", "--session-id=sess-1", "--model", "opus"}
	done := make(chan int, 1)
	go func() {
		done <- p.run(args, append(append(os.Environ(), "FAKE_ACCOUNT=first"), env...))
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
	if m := h.read(); m["type"] != "assistant" || m["error"] != nil {
		t.Fatalf("expected completed work to remain visible, got %v", m)
	}
	m := h.read()
	if m["type"] != "result" || m["is_error"] == true || m["prompt"] != continuePrompt {
		t.Fatalf("expected automatic completion without errors, replayed answers, or synthetic user echoes, got %v", m)
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

func TestStreamContinuationResumesLostAgents(t *testing.T) {
	h, done := startStream(t, func(session string, _ *time.Time) ([]string, []string, bool) {
		args := sessionArgs([]string{"-test.run=^TestFakeStreamClaude$", "--", "--output-format", "stream-json", "--input-format", "stream-json", "--session-id=sess-1"}, session)
		return args, append(os.Environ(), "FAKE_ACCOUNT=second"), true
	})
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "agents hit limit"}})
	for {
		m := h.read()
		if m["type"] != "result" {
			continue
		}
		if want := continuation([]string{"a1", "a2"}); m["prompt"] != want {
			t.Fatalf("continuation %q, want %q", m["prompt"], want)
		}
		break
	}
	h.in.Close()
	<-done
}

func TestStreamSwitchesAfterSubscriptionAccessDisabled(t *testing.T) {
	for _, trigger := range []string{"access denied", "access denied result", "subscription unavailable", "subscription unavailable result"} {
		t.Run(trigger, func(t *testing.T) {
			switches := 0
			h, done := startStream(t, func(session string, resetsAt *time.Time) ([]string, []string, bool) {
				switches++
				if session != "sess-1" || resetsAt != nil {
					t.Errorf("access failure is not a quota window: session %q, reset %v", session, resetsAt)
				}
				args := sessionArgs([]string{"-test.run=^TestFakeStreamClaude$", "--", "--model", "opus"}, session)
				return args, append(os.Environ(), "FAKE_ACCOUNT=second"), true
			})
			t.Cleanup(func() { h.in.Close() })
			h.send(map[string]any{"type": "control_request", "request_id": "init", "request": map[string]any{"subtype": "initialize"}})
			h.read()
			h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": trigger}})
			if m := h.read(); m["type"] != "assistant" || m["error"] != nil {
				t.Fatalf("completed work must remain visible: %v", m)
			}
			m := h.read()
			if m["type"] != "result" || m["is_error"] == true || !strings.HasPrefix(fmt.Sprint(m["result"]), "second ") || m["prompt"] != continuePrompt {
				t.Fatalf("expected resumed second account, got %v", m)
			}
			if switches != 1 {
				t.Fatalf("switches = %d, want 1", switches)
			}
			h.in.Close()
			if code := <-done; code != 0 {
				t.Fatalf("launcher exit = %d", code)
			}
		})
	}
}

func TestStreamDoesNotSwitchForOtherAccessText(t *testing.T) {
	for _, trigger := range []string{"permission error", "quoted denial"} {
		t.Run(trigger, func(t *testing.T) {
			h, done := startStream(t, func(string, *time.Time) ([]string, []string, bool) {
				t.Error("must not rotate for unrelated permission errors or ordinary assistant text")
				return nil, nil, false
			})
			t.Cleanup(func() { h.in.Close() })
			h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": trigger}})
			if m := h.read(); m["type"] != "assistant" {
				t.Fatalf("expected original assistant message, got %v", m)
			}
			if m := h.read(); m["type"] != "result" || (m["is_error"] == true) != (trigger == "permission error") {
				t.Fatalf("expected original result, got %v", m)
			}
			h.in.Close()
			<-done
		})
	}
}

func TestStreamStopsWhenReplacementAlsoHasSubscriptionAccessDisabled(t *testing.T) {
	switches := 0
	h, done := startStream(t, func(session string, resetsAt *time.Time) ([]string, []string, bool) {
		switches++
		if switches > 1 {
			return nil, nil, false
		}
		return sessionArgs([]string{"-test.run=^TestFakeStreamClaude$", "--"}, session), append(os.Environ(), "FAKE_ACCOUNT=second", "FAKE_ALWAYS_ACCESS_DENIED=1"), true
	})
	t.Cleanup(func() { h.in.Close() })
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "access denied"}})
	h.read() // completed work from the first account
	h.read() // completed work from the replacement
	if m := h.read(); m["type"] != "assistant" || m["error"] != "authentication_failed" {
		t.Fatalf("expected the final account's error, got %v", m)
	}
	if m := h.read(); m["type"] != "result" || m["is_error"] != true || switches != 2 {
		t.Fatalf("expected terminal access failure after two accounts, got %v (switches %d)", m, switches)
	}
	h.in.Close()
	<-done
}

func TestStreamStaysWithoutAnotherAccount(t *testing.T) {
	h, done := startStream(t, func(string, *time.Time) ([]string, []string, bool) { return nil, nil, false })
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hit limit"}})
	for _, typ := range []string{"assistant", "assistant", "rate_limit_event", "result"} {
		if m := h.read(); m["type"] != typ {
			t.Fatalf("expected exhausted account failure %q, got %v", typ, m)
		}
	}
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "continue"}})
	if m := h.read(); !strings.HasPrefix(fmt.Sprint(m["result"]), "first ") {
		t.Fatalf("expected the same process to answer, got %v", m)
	}
	h.in.Close()
	<-done
}

func TestStreamContinuesAcrossSeveralLimitedAccounts(t *testing.T) {
	switches := 0
	h, done := startStream(t, func(session string, _ *time.Time) ([]string, []string, bool) {
		switches++
		env := append(os.Environ(), fmt.Sprintf("FAKE_ACCOUNT=account-%d", switches))
		if switches == 1 {
			env = append(env, "FAKE_ALWAYS_LIMIT=1")
		}
		return []string{"-test.run=^TestFakeStreamClaude$", "--", "--resume=" + session}, env, true
	})
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hit limit"}})
	for range 2 {
		if m := h.read(); m["type"] != "assistant" || m["error"] != nil {
			t.Fatalf("expected completed work, got %v", m)
		}
	}
	if m := h.read(); m["type"] != "result" || !strings.HasPrefix(fmt.Sprint(m["result"]), "account-2 ") || m["prompt"] != continuePrompt {
		t.Fatalf("expected automatic completion on the third account, got %v", m)
	}
	h.in.Close()
	<-done
}

func TestStreamStopsWhenReplacementIsAlsoLimited(t *testing.T) {
	switches := 0
	h, done := startStream(t, func(session string, _ *time.Time) ([]string, []string, bool) {
		switches++
		if switches > 1 {
			return nil, nil, false
		}
		return []string{"-test.run=^TestFakeStreamClaude$", "--", "--resume=" + session}, append(os.Environ(), "FAKE_ACCOUNT=second", "FAKE_ALWAYS_LIMIT=1"), true
	})
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hit limit"}})
	for _, typ := range []string{"assistant", "assistant", "assistant", "rate_limit_event", "result"} {
		if m := h.read(); m["type"] != typ || typ == "result" && m["is_error"] != true {
			t.Fatalf("expected the final exhausted account failure, got %v", m)
		}
	}
	h.in.Close()
	<-done
}

func TestStreamDoesNotSwitchForUnconfirmedThrottling(t *testing.T) {
	h, done := startStream(t, func(string, *time.Time) ([]string, []string, bool) {
		t.Error("ordinary throttling must not switch accounts")
		return nil, nil, false
	})
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "throttle"}})
	if m := h.read(); m["type"] != "assistant" || m["error"] != "rate_limit" {
		t.Fatalf("expected the throttling error, got %v", m)
	}
	if m := h.read(); m["type"] != "result" || m["is_error"] != true {
		t.Fatalf("expected the failed result, got %v", m)
	}
	h.in.Close()
	<-done
}

func TestStreamContinuesAfterHostEOF(t *testing.T) {
	h, done := startStream(t, func(session string, _ *time.Time) ([]string, []string, bool) {
		return []string{"-test.run=^TestFakeStreamClaude$", "--", "--resume=" + session}, append(os.Environ(), "FAKE_ACCOUNT=second"), true
	})
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hit limit"}})
	h.in.Close()
	h.read()
	if m := h.read(); m["type"] != "result" || m["prompt"] != continuePrompt {
		t.Fatalf("expected automatic completion after host EOF, got %v", m)
	}
	<-done
}

func TestStreamInterruptDuringSwitchDoesNotContinue(t *testing.T) {
	selecting, selected := make(chan struct{}), make(chan struct{})
	h, done := startStream(t, func(session string, _ *time.Time) ([]string, []string, bool) {
		close(selecting)
		<-selected
		return []string{"-test.run=^TestFakeStreamClaude$", "--", "--resume=" + session}, append(os.Environ(), "FAKE_ACCOUNT=second"), true
	})
	h.send(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "hit limit"}})
	h.read()
	<-selecting
	h.send(map[string]any{"type": "control_request", "request_id": "cancel", "request": map[string]string{"subtype": "interrupt"}})
	// A later request is a barrier: readHost handles the interrupt before it.
	h.send(map[string]any{"type": "control_request", "request_id": "barrier", "request": map[string]string{"subtype": "set_model", "model": "opus"}})
	close(selected)
	for _, typ := range []string{"assistant", "rate_limit_event", "result", "control_response", "control_response"} {
		if m := h.read(); m["type"] != typ {
			t.Fatalf("expected cancellation without automatic continuation, got %v", m)
		}
	}
	h.in.Close()
	<-done
}

// A host's first request can exceed a pipe's buffer (T3 Code's initialize
// is 8 KiB; Windows pipes hold 4 KiB) while Claude is still writing its
// startup output: neither side may wait on the other.
func TestStreamLargeMessagesDoNotDeadlock(t *testing.T) {
	init, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": "r1", "request": map[string]any{"subtype": "initialize", "systemPrompt": strings.Repeat("y", 256<<10)}})
	h, done := startStreamWith(t, func(string, *time.Time) ([]string, []string, bool) { return nil, nil, false }, append(init, '\n'), "FAKE_NOISY=1")
	for range 64 {
		if m := h.read(); m["subtype"] != "hook_response" {
			t.Fatalf("expected startup output, got %v", m)
		}
	}
	if m := h.read(); m["type"] != "control_response" {
		t.Fatalf("initialize answered with %v", m)
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

func TestAccountAccessDisabledCodes(t *testing.T) {
	for _, tt := range []struct {
		name string
		line string
		want bool
	}{
		{"assistant error", `{"type":"assistant","error":"oauth_org_not_allowed"}`, true},
		{"assistant API code", `{"type":"assistant","error":"authentication_failed","api_error_code":"oauth_not_allowed_for_organization"}`, true},
		{"result API code", `{"type":"result","is_error":true,"api_error_code":"oauth_not_allowed_for_organization"}`, true},
		{"unrelated error", `{"type":"assistant","error":"permission_denied","api_error_code":"permission_error"}`, false},
		{"ordinary assistant", `{"type":"assistant","api_error_code":"oauth_not_allowed_for_organization"}`, false},
		{"successful result", `{"type":"result","is_error":false,"api_error_code":"oauth_not_allowed_for_organization"}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var line streamLine
			if err := json.Unmarshal([]byte(tt.line), &line); err != nil {
				t.Fatal(err)
			}
			if got := accountAccessDisabled(line); got != tt.want {
				t.Fatalf("accountAccessDisabled = %v, want %v", got, tt.want)
			}
		})
	}
}
