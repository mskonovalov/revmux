package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	cursorAPIBase = "https://api.cursor.com"
	//nolint:gosec // G101: cursorAPIKeyEnv is the environment variable name, not a credential
	cursorAPIKeyEnv    = "CURSOR_API_KEY"
	cursorCloudRepoEnv = "REVMUX_CURSOR_CLOUD_REPO"
	cursorCloudPREnv   = "REVMUX_CURSOR_CLOUD_PR"
)

// CursorCloud runs reviews via the Cursor Cloud Agents API (public beta).
// It creates an agent run, polls for completion, and extracts structured findings.
type CursorCloud struct {
	httpClient *http.Client
	workDir    string
	clock      Clock
}

// NewCursorCloud builds a cursor-cloud executor.
func NewCursorCloud(opts Opts) *CursorCloud {
	clock := opts.Clock
	if clock == nil {
		clock = NewClock()
	}
	return &CursorCloud{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		workDir:    opts.WorkDir,
		clock:      clock,
	}
}

// Authenticated checks if CURSOR_API_KEY is set and valid by calling GET /v1/models.
func (c *CursorCloud) Authenticated(ctx context.Context) (bool, error) {
	apiKey := strings.TrimSpace(os.Getenv(cursorAPIKeyEnv))
	if apiKey == "" {
		return false, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cursorAPIBase+"/v1/models", http.NoBody)
	if err != nil {
		return false, fmt.Errorf("cursor-cloud auth: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("cursor-cloud auth: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("cursor-cloud auth: unexpected status %d", resp.StatusCode)
	}

	return true, nil
}

// Login prints instructions to create an API key in Cursor Dashboard.
func (c *CursorCloud) Login(_ context.Context, _ io.ReadWriter) error {
	return fmt.Errorf("set %s to your Cursor API key from https://cursor.com/settings/api-keys", cursorAPIKeyEnv)
}

// Run executes one review request via the Cloud Agents API.
func (c *CursorCloud) Run(ctx context.Context, req Request, sink EventSink) (Result, error) {
	apiKey := strings.TrimSpace(os.Getenv(cursorAPIKeyEnv))
	if apiKey == "" {
		return Result{RequestedModel: req.Model}, errors.New("cursor-cloud: CURSOR_API_KEY not set")
	}

	// Resolve target repository
	repoURL, prURL, err := c.resolveRepo(ctx)
	if err != nil {
		return Result{RequestedModel: req.Model}, err
	}

	// Build prompt with schema contract if needed
	prompt := req.Prompt
	if len(req.Schema) > 0 {
		prompt += "\n\nReturn ONLY a JSON object matching the schema below. No prose before or after it.\n\nSchema:\n" +
			string(req.Schema) + "\n"
	}

	// Create agent run
	c.emit(sink, Event{Kind: EventInfo, Text: "creating cloud agent run"})
	agentID, runID, err := c.createAgent(ctx, apiKey, prompt, req.Model, req.Effort, repoURL, prURL)
	if err != nil {
		return Result{RequestedModel: req.Model}, err
	}

	c.emit(sink, Event{Kind: EventInfo, Text: "agent: " + agentID})
	c.emit(sink, Event{Kind: EventProgress, Text: "waiting for completion"})

	// Poll for completion
	res, err := c.pollCompletion(ctx, apiKey, agentID, runID, sink)
	res.RequestedModel = req.Model
	return res, err
}

// resolveRepo determines the target repository URL and optional PR URL.
func (c *CursorCloud) resolveRepo(ctx context.Context) (repoURL, prURL string, err error) {
	// Check explicit repo env var first
	repoURL = strings.TrimSpace(os.Getenv(cursorCloudRepoEnv))
	if repoURL == "" {
		// Derive from git remote
		cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
		cmd.Dir = c.workDir
		out, gitErr := cmd.Output()
		if gitErr != nil {
			return "", "", errors.New("cursor-cloud: no repo URL; set REVMUX_CURSOR_CLOUD_REPO or run in a git repository")
		}
		repoURL = strings.TrimSpace(string(out))
	}

	if repoURL == "" {
		return "", "", errors.New("cursor-cloud: empty repo URL")
	}

	// Optionally attach PR
	prURL = strings.TrimSpace(os.Getenv(cursorCloudPREnv))

	return repoURL, prURL, nil
}

// createAgent calls POST /v1/agents to start a review run.
func (c *CursorCloud) createAgent(ctx context.Context, apiKey, prompt, model, effort, repoURL, prURL string) (agentID, runID string, err error) {
	body := map[string]any{
		"prompt": map[string]string{
			"text": prompt,
		},
		"repos": []map[string]string{
			{"url": repoURL},
		},
	}

	if prURL != "" {
		body["repos"] = []map[string]string{
			{
				"url":   repoURL,
				"prUrl": prURL,
			},
		}
	}

	if model != "" {
		modelConfig := map[string]any{
			"id": model,
		}
		// Map effort to model params if supported (placeholder - adjust per docs)
		if effort != "" {
			modelConfig["params"] = map[string]string{
				"reasoning_effort": effort,
			}
		}
		body["model"] = modelConfig
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", "", fmt.Errorf("cursor-cloud: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorAPIBase+"/v1/agents", bytes.NewReader(jsonBody))
	if err != nil {
		return "", "", fmt.Errorf("cursor-cloud: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("cursor-cloud: create agent: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("cursor-cloud: create agent failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		AgentID string `json:"agentId"`
		RunID   string `json:"runId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("cursor-cloud: decode response: %w", err)
	}

	return result.AgentID, result.RunID, nil
}

// pollCompletion polls the agent status until terminal, with backoff.
func (c *CursorCloud) pollCompletion(ctx context.Context, apiKey, agentID, runID string, sink EventSink) (Result, error) {
	backoff := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second}
	attempt := 0

	for {
		select {
		case <-ctx.Done():
			return Result{}, fmt.Errorf("cursor-cloud wait: %w", ctx.Err())
		default:
		}

		status, finished, output, actualModel, err := c.checkStatus(ctx, apiKey, agentID)
		if err != nil {
			return Result{}, err
		}

		if finished {
			c.emit(sink, Event{Kind: EventFinished, Text: status})
			res := Result{
				ActualModel: actualModel,
			}
			if len(output) > 0 {
				res.StructuredOutput = output
			} else {
				res.Raw = string(output)
			}
			return res, nil
		}

		// Emit progress
		if status != "" {
			c.emit(sink, Event{Kind: EventActivity, Text: status})
		}

		// Wait with backoff
		delay := backoff[len(backoff)-1]
		if attempt < len(backoff) {
			delay = backoff[attempt]
			attempt++
		}

		timer := c.clock.AfterFunc(delay, func() {})
		select {
		case <-ctx.Done():
			timer.Stop()
			return Result{}, fmt.Errorf("cursor-cloud wait: %w", ctx.Err())
		case <-time.After(delay):
			// Continue polling
		}
	}
}

// checkStatus queries the agent/run status and extracts output if complete.
func (c *CursorCloud) checkStatus(ctx context.Context, apiKey, agentID string) (status string, finished bool, output json.RawMessage, model string, err error) {
	// GET /v1/agents/{id} returns the agent with its latest run status
	url := fmt.Sprintf("%s/v1/agents/%s", cursorAPIBase, agentID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", false, nil, "", fmt.Errorf("cursor-cloud: build status request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", false, nil, "", fmt.Errorf("cursor-cloud: check status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", false, nil, "", fmt.Errorf("cursor-cloud: check status failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Status string `json:"status"`
		Model  struct {
			ID string `json:"id"`
		} `json:"model"`
		Result json.RawMessage `json:"result"` // Final output when run is FINISHED
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", false, nil, "", fmt.Errorf("cursor-cloud: decode status: %w", err)
	}

	// Run status: CREATING, RUNNING, WAITING_FOR_BACKGROUND_WORK, FINISHED, ERROR, CANCELLED, EXPIRED
	// Check if terminal - compare case-insensitively to handle both spellings
	statusUpper := strings.ToUpper(result.Status)
	//nolint:misspell // API uses CANCELLED (British spelling) as status value
	terminal := statusUpper == "FINISHED" || statusUpper == "ERROR" || statusUpper == "CANCELLED" || statusUpper == "CANCELED" || statusUpper == "EXPIRED"

	// Extract structured output from result field if finished
	var structuredOutput json.RawMessage
	if terminal && len(result.Result) > 0 {
		// Try to extract JSON from the result field
		if extracted := c.extractJSON(string(result.Result)); extracted != nil {
			structuredOutput = extracted
		}
	}

	return result.Status, terminal, structuredOutput, result.Model.ID, nil
}

// extractJSON attempts to extract a JSON object from text (similar to codex extract).
func (c *CursorCloud) extractJSON(text string) json.RawMessage {
	for i, ch := range text {
		if ch != '{' {
			continue
		}
		var out json.RawMessage
		err := json.NewDecoder(strings.NewReader(text[i:])).Decode(&out)
		if err == nil {
			return out
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
	}
	return nil
}

// emit sends an event if the sink is non-nil.
func (c *CursorCloud) emit(sink EventSink, ev Event) {
	if sink != nil {
		sink.Emit(ev)
	}
}
