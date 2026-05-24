package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/danmestas/orch/internal/synadia"
)

// runTell is `orch tell`. Replaces bin/orch-tell:
//
//	orch tell [--force] [--collect] [--timeout N] <pane|alias> <prompt>
//	orch tell --list
//	orch tell <pane|alias> -                 # prompt from stdin
//
// --collect streams §6.2 response chunks to stdout as they arrive and
// maps §9 Nats-Service-Error-Code headers to the exit codes documented
// in internal/synadia (ExitCodeForServiceError).
func runTell(args []string) error {
	fs := flag.NewFlagSet("tell", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we print our own usage
	var (
		force   = fs.Bool("force", false, "override worker→observer guard")
		collect = fs.Bool("collect", false, "request/reply mode; stream §6.2 response chunks to stdout")
		timeout = fs.Int("timeout", 0, "--collect inactivity window in seconds (default ORCH_TELL_INACTIVITY_TIMEOUT or 30)")
		list    = fs.Bool("list", false, "list tmux panes and their current commands")
		natsURL = fs.String("nats", "", "NATS URL override")
	)
	if err := fs.Parse(args); err != nil {
		return tellUsage(err)
	}

	if *list {
		return tellListPanes()
	}

	rest := fs.Args()
	if len(rest) < 2 {
		return tellUsage(fmt.Errorf("usage: orch tell [--force] [--collect] [--timeout N] <pane|alias> <prompt> | --list"))
	}

	target := rest[0]
	prompt, err := readPrompt(rest[1:])
	if err != nil {
		return err
	}

	// Inactivity window for --collect (kept identical to bin/orch-tell).
	inactivity := 30
	if env := os.Getenv("ORCH_TELL_INACTIVITY_TIMEOUT"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			inactivity = n
		}
	}
	if *timeout > 0 {
		inactivity = *timeout
	}

	nc, err := connectNATS(*natsURL, "orch-tell")
	if err != nil {
		return err
	}
	if nc != nil {
		defer nc.Close()
	}

	workers, err := snapshotOnce(context.Background(), nc)
	if err != nil {
		return fmt.Errorf("registry snapshot: %w", err)
	}

	worker, ok := lookupTarget(workers, target)
	if !ok {
		return &exitError{code: 1, msg: fmt.Sprintf("target %q not in the orch registry — check alias file or run orch spawn", target)}
	}
	if worker.Subjects.Prompt == "" {
		return &exitError{code: 1, msg: fmt.Sprintf("registry returned worker without prompt subject (target: %s — shim may not have advertised endpoints)", target)}
	}

	// Observer-exclusion guard (ADR-0002): a worker pane (ORCH_PANE_ID
	// set in env) is refused when telling an observer-class target
	// unless --force is passed. Operator panes (no ORCH_PANE_ID) are
	// unrestricted.
	if os.Getenv("ORCH_PANE_ID") != "" && !*force && worker.Role == "observer" {
		return &exitError{
			code: 1,
			msg:  fmt.Sprintf("refusing to tell observer pane %s (sender is worker %s); pass --force to override", worker.PaneID, os.Getenv("ORCH_PANE_ID")),
		}
	}

	if *collect {
		if nc == nil {
			// Fixture mode (ORCH_REGISTRY_FIXTURE_FILE set) cannot
			// stream chunks — there is no NATS connection. Tell the
			// caller the test path bypasses --collect.
			return &exitError{code: 1, msg: "--collect requires a live NATS connection; ORCH_REGISTRY_FIXTURE_FILE skips that path"}
		}
		return tellCollect(nc, worker.Subjects.Prompt, prompt, time.Duration(inactivity)*time.Second)
	}

	// Fixture mode: no NATS connection, but the guard and lookup
	// produced a worker. The bash-era tests exercised exactly this
	// shape (publish path silent on a stub bus), so treat the publish
	// as a no-op success. writeSendLog still runs — that's the side
	// effect tests assert on.
	if nc == nil {
		writeSendLog(worker.PaneID, prompt)
		return nil
	}

	if err := nc.Publish(worker.Subjects.Prompt, []byte(prompt)); err != nil {
		return fmt.Errorf("publish to %s: %w", worker.Subjects.Prompt, err)
	}
	if err := nc.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	writeSendLog(worker.PaneID, prompt)
	return nil
}

// tellListPanes shells out to tmux list-panes for the --list shape.
// Kept lean — this is a diagnostic helper, not an alias resolver.
func tellListPanes() error {
	cmd := exec.Command("tmux", "list-panes", "-a",
		"-F", "#{pane_id}  #{session_name}:#{window_index}.#{pane_index}  #{pane_current_command}  #{pane_current_path}")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux list-panes failed: %w", err)
	}
	return nil
}

// readPrompt collects the prompt text from positional args, or from
// stdin when the single positional is "-". Matches bin/orch-tell.
func readPrompt(args []string) (string, error) {
	if len(args) == 1 && args[0] == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	}
	return strings.Join(args, " "), nil
}

// tellCollect implements --collect: send the prompt as a request and
// stream each §6.2 response chunk's .data to stdout until the §6.5
// terminator (or inactivity timeout) fires.
//
// Error path: §9 Nats-Service-Error[-Code] headers map to exit codes
// via synadia.ExitCodeForServiceError; the optional §9.1 JSON body is
// surfaced on stderr if present.
func tellCollect(nc *nats.Conn, subject string, prompt string, inactivity time.Duration) error {
	inbox := nats.NewInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return fmt.Errorf("subscribe to %s: %w", inbox, err)
	}
	defer sub.Unsubscribe() //nolint:errcheck
	if err := nc.PublishRequest(subject, inbox, []byte(prompt)); err != nil {
		return fmt.Errorf("publish request to %s: %w", subject, err)
	}
	if err := nc.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}

	var (
		errCode      int
		errMsg       string
		sawErrHeader bool
	)
	for {
		msg, err := sub.NextMsg(inactivity)
		if err != nil {
			// Inactivity timeout — natural stream end if the shim
			// already sent its terminator. Surface as success unless
			// an error header arrived first.
			if sawErrHeader {
				return surfaceServiceError(errCode, errMsg, "")
			}
			return nil
		}

		// §9 error path: headers carry Nats-Service-Error-Code (+ optional
		// JSON body {error, message, retry_after_s}).
		if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
			n, _ := strconv.Atoi(code)
			errCode = n
			errMsg = msg.Header.Get("Nats-Service-Error")
			sawErrHeader = true
			// Read body if present — may carry §9.1 JSON detail.
			detail := ""
			if len(msg.Data) > 0 {
				detail = string(msg.Data)
			}
			return surfaceServiceError(errCode, errMsg, detail)
		}

		// §6.5 terminator → end of stream.
		if synadia.IsTerminator(msg) {
			return nil
		}

		// §6.2 response chunk → stream .data to stdout.
		// §6.6 unknown chunk types are silently dropped.
		var chunk struct {
			Type string `json:"type"`
			Data string `json:"data"`
		}
		if err := json.Unmarshal(msg.Data, &chunk); err != nil {
			continue
		}
		if chunk.Type == "response" {
			_, _ = io.WriteString(os.Stdout, chunk.Data)
		}
	}
}

// surfaceServiceError prints the error header info (and JSON body detail
// when present) to stderr and returns an *exitError with the code from
// internal/synadia's mapping. Matches bin/orch-tell's stderr shape.
func surfaceServiceError(code int, headerMsg string, body string) error {
	fmt.Fprintf(os.Stderr, "orch tell: agent error %d: %s\n", code, headerMsg)
	if body != "" {
		var bodyJSON struct {
			Message    string `json:"message"`
			RetryAfter int    `json:"retry_after_s"`
		}
		if err := json.Unmarshal([]byte(body), &bodyJSON); err == nil {
			if bodyJSON.Message != "" {
				fmt.Fprintf(os.Stderr, "orch tell: agent error detail: %s\n", bodyJSON.Message)
			}
			if bodyJSON.RetryAfter > 0 {
				fmt.Fprintf(os.Stderr, "orch tell: retry after %ds\n", bodyJSON.RetryAfter)
			}
		}
	}
	return &exitError{code: synadia.ExitCodeForServiceError(code)}
}

// writeSendLog appends a JSON entry to ORCH_SEND_LOG (~/.cache/orch-send.log
// by default) so the operator can distinguish prompts the orchestrator sent
// from prompts the user typed when returning from a restart. Best-effort —
// errors are silently dropped (matches bin/orch-tell behaviour).
func writeSendLog(pane string, prompt string) {
	logPath := os.Getenv("ORCH_SEND_LOG")
	if logPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		logPath = filepath.Join(home, ".cache", "orch-send.log")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return
	}
	sender := os.Getenv("ORCH_PANE_ID")
	if sender == "" {
		sender = fmt.Sprintf("orchestrator-%d", os.Getppid())
	}
	preview := prompt
	if len(preview) > 200 {
		preview = preview[:200]
	}
	entry := map[string]any{
		"ts_ns":          time.Now().UnixNano(),
		"pane":           pane,
		"sender":         sender,
		"prompt_preview": preview,
		"prompt_len":     len(prompt),
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	_, _ = f.Write(append(b, '\n'))
}

func tellUsage(cause error) error {
	if cause != nil {
		fmt.Fprintln(os.Stderr, "orch tell:", cause)
	}
	return &exitError{code: 1}
}
