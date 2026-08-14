package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// qoderClient talks to the Qoder Cloud Agents API.
// It creates a fresh Session per Generate call, sends a user.message,
// and polls the events endpoint until an agent.message arrives.
type qoderClient struct {
	baseURL    string
	apiKey     string
	agentID    string
	envID      string
	httpClient *http.Client
}

// newQoderClient builds a client from an AlertMesh LLMProvider row.
// The "model" field is expected to carry "agent_id|environment_id".
func newQoderClient(baseURL, apiKey, model string) (*qoderClient, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.qoder.com"
	}
	agentID, envID, err := parseQoderModel(model)
	if err != nil {
		return nil, err
	}
	return &qoderClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		agentID: agentID,
		envID:   envID,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}, nil
}

func parseQoderModel(model string) (agentID, envID string, err error) {
	parts := strings.SplitN(model, "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf(`qoder model field must be "agent_id|environment_id", got %q`, model)
	}
	agentID = strings.TrimSpace(parts[0])
	envID = strings.TrimSpace(parts[1])
	if agentID == "" || envID == "" {
		return "", "", fmt.Errorf("qoder agent_id and environment_id must not be empty")
	}
	return agentID, envID, nil
}

// Generate sends a single prompt to a fresh Qoder Cloud Agent Session and
// returns the first text response from the agent.
func (c *qoderClient) Generate(ctx context.Context, prompt string) (string, error) {
	sessionID, err := c.createSession(ctx)
	if err != nil {
		return "", fmt.Errorf("create qoder session: %w", err)
	}

	if err := c.sendMessage(ctx, sessionID, prompt); err != nil {
		return "", fmt.Errorf("send qoder message: %w", err)
	}

	reply, err := c.pollForText(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("poll qoder response: %w", err)
	}
	return reply, nil
}

func (c *qoderClient) createSession(ctx context.Context) (string, error) {
	body := map[string]any{
		"agent": map[string]any{
			"id":   c.agentID,
			"type": "agent",
		},
		"environment_id": c.envID,
		"title":          "AlertMesh observability query",
	}
	var resp qoderSession
	if err := c.doRequest(ctx, http.MethodPost, "/api/v1/cloud/sessions", body, &resp); err != nil {
		return "", err
	}
	if resp.ID == "" {
		return "", fmt.Errorf("qoder session id is empty")
	}
	return resp.ID, nil
}

func (c *qoderClient) sendMessage(ctx context.Context, sessionID, prompt string) error {
	body := map[string]any{
		"events": []map[string]any{
			{
				"type": "user.message",
				"content": []map[string]any{
					{"type": "text", "text": prompt},
				},
			},
		},
	}
	path := "/api/v1/cloud/sessions/" + sessionID + "/events"
	return c.doRequest(ctx, http.MethodPost, path, body, nil)
}

func (c *qoderClient) pollForText(ctx context.Context, sessionID string) (string, error) {
	path := "/api/v1/cloud/sessions/" + sessionID + "/events"
	deadline := time.Now().Add(240 * time.Second)
	for time.Now().Before(deadline) {
		var list qoderEventList
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &list); err != nil {
			return "", err
		}
		for _, evt := range list.Data {
			if evt.Type == "agent.message" {
				for _, block := range evt.Content {
					if block.Type == "text" {
						return block.Text, nil
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return "", fmt.Errorf("timeout waiting for qoder agent response")
}

func (c *qoderClient) doRequest(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()

	resBody, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 400 {
		return fmt.Errorf("qoder API error %d: %s", res.StatusCode, string(resBody))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resBody, out); err != nil {
		return fmt.Errorf("decode qoder response: %w (body: %s)", err, string(resBody))
	}
	return nil
}

type qoderSession struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type qoderEventList struct {
	Data []qoderEvent `json:"data"`
}

type qoderEvent struct {
	ID      string             `json:"id"`
	Type    string             `json:"type"`
	Content []qoderContentBlock `json:"content"`
}

type qoderContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// TestQoderProvider sends a ping message to a fresh Qoder Cloud Agent Session
// and returns the first text reply. It is used by the LLM provider test endpoint.
func TestQoderProvider(ctx context.Context, baseURL, apiKey, model string) (string, error) {
	qc, err := newQoderClient(baseURL, apiKey, model)
	if err != nil {
		return "", err
	}
	return qc.Generate(ctx, "ping")
}
