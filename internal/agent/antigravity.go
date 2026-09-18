package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/kunchenguid/no-mistakes/internal/shellenv"
)

// antigravityAgent spawns the agy CLI (Antigravity) for each invocation.
// In print mode (-p), agy streams NDJSON events on stdout when
// --output-format stream-json is configured and reports final token usage
// and structured output in the terminal result event.
type antigravityAgent struct {
	bin       string
	extraArgs []string
}

// Name returns the canonical agent name "agy".
func (a *antigravityAgent) Name() string { return "agy" }

// SupportsSessionResume reports agy's native durable-session capability:
// agy assigns a conversation_id to each session and resumes it via --conversation <id>.
func (a *antigravityAgent) SupportsSessionResume() bool { return true }

// ReportsAgentAttempts indicates that the agy adapter reports attempt counts.
func (a *antigravityAgent) ReportsAgentAttempts() bool { return true }

// Run executes the agy CLI with retry on transient failures.
func (a *antigravityAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	return runWithRetry(ctx, "agy", opts, claudeMaxRetries, classifyTransient, nil, func() (*Result, error) {
		return a.runOnce(ctx, opts)
	})
}

// Close releases any resources associated with the agent.
func (a *antigravityAgent) Close() error { return nil }

func (a *antigravityAgent) runOnce(ctx context.Context, opts RunOpts) (*Result, error) {
	resumeID := ""
	if opts.Session != nil {
		resumeID = opts.Session.ID
	}
	args := a.buildArgs(opts.Prompt, opts.JSONSchema, resumeID)
	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Dir = opts.CWD
	cmd.Stdin = nil
	cmd.Env = gitSafeEnv(opts.CWD)
	shellenv.ConfigureShellCommand(cmd)

	var stderrBuf []byte
	var stderrWG sync.WaitGroup
	started, err := startNativeAgentCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("agy start: %w", err)
	}
	defer started.closePipes()
	pid := started.pid()
	emitAgentStarted(opts, "agy", pid)

	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		stderrBuf, _ = io.ReadAll(started.stderr)
	}()

	var usage TokenUsage
	var result *agyResult
	var streamText string
	if err := parseAgyEvents(ctx, started.stdout, opts.OnChunk, &usage, &result, &streamText); err != nil {
		err = started.waitAfterParseError(err)
		stderrWG.Wait()
		retErr := fmt.Errorf("agy parse events: %w", err)
		emitAgentExited(opts, "agy", pid, retErr)
		return nil, retErr
	}

	waitErr := started.wait()
	stderrWG.Wait()
	if waitErr != nil {
		retErr := fmt.Errorf("agy exited: %w: %s", waitErr, strings.TrimSpace(string(stderrBuf)))
		emitAgentExited(opts, "agy", pid, retErr)
		return nil, retErr
	}

	if result == nil {
		retErr := fmt.Errorf("agy returned no result event: %s", strings.TrimSpace(string(stderrBuf)))
		emitAgentExited(opts, "agy", pid, retErr)
		return nil, retErr
	}

	res, err := finalizeAgyResult(result, opts.JSONSchema, usage)
	if res != nil {
		res.SessionID = result.conversationID
		res.Resumed = resumeID != ""
		res.Model = result.model
		res.ModelProvider = "google"
		res.SessionUsageCumulative = resumeID != ""
	}
	emitAgentExited(opts, "agy", pid, err)
	return res, err
}

func (a *antigravityAgent) buildArgs(prompt string, schema json.RawMessage, resumeID string) []string {
	args := make([]string, 0, len(a.extraArgs)+10)
	args = append(args, a.extraArgs...)
	args = append(args,
		"-p", prompt,
		"--output-format", "stream-json",
	)
	if resumeID != "" {
		args = append(args, "--conversation", resumeID)
	}
	if len(schema) > 0 {
		args = append(args, "--json-schema", string(schema))
	}
	args = append(args, "--dangerously-skip-permissions")
	return args
}

type agyTopLevelEvent struct {
	Event          string          `json:"event"`
	ConversationID string          `json:"conversation_id,omitempty"`
	StepUpdate     *agyStepUpdate  `json:"step_update,omitempty"`
	Result         *agyResultEvent `json:"result,omitempty"`
}

type agyStepUpdate struct {
	ConversationID string    `json:"conversation_id,omitempty"`
	StepIndex      int       `json:"step_index"`
	State          string    `json:"state"`
	StepType       string    `json:"step_type"`
	TextDelta      string    `json:"text_delta,omitempty"`
	Usage          *agyUsage `json:"usage,omitempty"`
}

type agyResultEvent struct {
	ConversationID   string          `json:"conversation_id,omitempty"`
	Status           string          `json:"status"`
	Response         string          `json:"response"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	Usage            *agyUsage       `json:"usage,omitempty"`
}

type agyUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

type agyResult struct {
	status           string
	response         string
	structuredOutput json.RawMessage
	conversationID   string
	model            string
}

func parseAgyEvents(ctx context.Context, r io.Reader, onChunk func(string), usage *TokenUsage, result **agyResult, streamText *string) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024*1024)

	var fullText strings.Builder
	convID := ""

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event agyTopLevelEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}

		if event.ConversationID != "" {
			convID = event.ConversationID
		}

		switch event.Event {
		case "init":
			if event.ConversationID != "" {
				convID = event.ConversationID
			}
		case "step_update":
			if event.StepUpdate != nil {
				if event.StepUpdate.ConversationID != "" {
					convID = event.StepUpdate.ConversationID
				}
				if event.StepUpdate.TextDelta != "" {
					fullText.WriteString(event.StepUpdate.TextDelta)
					if onChunk != nil {
						onChunk(event.StepUpdate.TextDelta)
					}
				}
			}
		case "result":
			if event.Result != nil {
				if event.Result.ConversationID != "" {
					convID = event.Result.ConversationID
				}
				if event.Result.Usage != nil {
					usage.InputTokens = event.Result.Usage.InputTokens
					usage.OutputTokens = event.Result.Usage.OutputTokens
					usage.CacheReadTokens = event.Result.Usage.CacheReadTokens
					usage.ReasoningTokens = event.Result.Usage.ThinkingTokens
					usage.Reported = true
				}
				*result = &agyResult{
					status:           event.Result.Status,
					response:         event.Result.Response,
					structuredOutput: event.Result.StructuredOutput,
					conversationID:   convID,
				}
			}
		}
	}

	*streamText = fullText.String()
	return scanner.Err()
}

func finalizeAgyResult(res *agyResult, schema json.RawMessage, usage TokenUsage) (*Result, error) {
	if res.status != "SUCCESS" {
		return nil, fmt.Errorf("agy status: %s (response: %q)", res.status, outputSnippet(res.response))
	}
	text := res.response
	if text == "" {
		return nil, fmt.Errorf("agy returned empty response")
	}
	if len(schema) == 0 {
		return &Result{
			Text:                  text,
			Usage:                 usage,
			UsageReported:         usage.Reported,
			CacheCreationReported: false,
		}, nil
	}

	if len(res.structuredOutput) > 0 && string(res.structuredOutput) != "null" {
		if err := validateStructuredOutput(res.structuredOutput, schema); err == nil {
			return &Result{
				Output:                res.structuredOutput,
				Text:                  text,
				Usage:                 usage,
				UsageReported:         usage.Reported,
				CacheCreationReported: false,
			}, nil
		}
	}

	return finalizeTextResult("agy", text, schema, usage)
}
