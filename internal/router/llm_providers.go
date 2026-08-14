package router

// CRUD + management endpoints for /api/v1/llm-providers, the admin-only
// surface that lets operators register the LLM backend used by the AI agent
// (root-cause analysis, follow-up chat, …).
//
// Storage rules:
//
//   - The plaintext API key NEVER leaves the server in any GET/list response;
//     it is masked to the literal "******".  PUT requests that send the
//     placeholder string are interpreted as "do not change" and the existing
//     ciphertext is preserved.
//   - On write the field is AES-256-GCM encrypted at rest using the same
//     master key the rest of the platform uses (config.EncryptionKey).
//   - Decryption falls back to plaintext for backwards compatibility with
//     rows that were inserted directly via SQL during early development;
//     `internal/ai/agent.go::decryptAPIKey` follows the same convention.
//   - At most one row may have is_default=true; flipping the default is a
//     dedicated endpoint that runs the swap atomically.
//
// Wire transport rules (browser ⇄ API):
//
//   - The browser RSA-encrypts api_key with the system public key and sends
//     it prefixed with "ENC:" (see web/src/api/crypto.ts::encryptSecret +
//     auth/wire_crypto.go::DecodeClientCipher).  Plaintext payloads are
//     still accepted so legacy clients / curl scripts keep working, but
//     the UI MUST send the ENC: variant so the secret never appears in
//     plaintext in browser DevTools / proxy logs / WAF traces.
//   - Note: model.LLMProvider.APIKey carries `json:"-"` on purpose, so we
//     never accidentally bind / serialise the ciphertext directly.  All
//     inbound + outbound shaping goes through the DTOs below.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	restful "github.com/emicklei/go-restful/v3"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
	"gorm.io/gorm"

	"github.com/kuzane/alertmesh/internal/ai"
	"github.com/kuzane/alertmesh/internal/auth"
	cfgcrypto "github.com/kuzane/alertmesh/internal/config"
	"github.com/kuzane/alertmesh/internal/httputil"
	"github.com/kuzane/alertmesh/internal/label"
	"github.com/kuzane/alertmesh/internal/model"
)

// apiKeyMask is the placeholder the UI sees in list/get responses so the
// raw API key never reaches the browser.  PUT requests that echo back the
// placeholder are treated as "keep existing".
const apiKeyMask = "******"

// registerLLMProviderRoutes wires the admin-only LLM-provider management
// surface onto the AI handler so it lives next to the rest of the AI APIs.
func (h *aiHandler) registerLLMProviderRoutes(ws *restful.WebService) {
	ws.Route(ws.GET("/llm-providers").To(h.listLLMProviders).
		Doc("List LLM providers (api_key masked)").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "LLMProvider").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.POST("/llm-providers").To(h.createLLMProvider).
		Doc("Create LLM provider").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "LLMProvider").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.PUT("/llm-providers/{id}").To(h.updateLLMProvider).
		Doc("Update LLM provider (api_key '******' keeps existing value)").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "LLMProvider").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.POST("/llm-providers/{id}/set-default").To(h.setDefaultLLMProvider).
		Doc("Mark this provider as the default; clears is_default on all others").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "LLMProvider").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.POST("/llm-providers/{id}/test").To(h.testLLMProvider).
		Doc("Send a tiny prompt to verify credentials / base_url / model are reachable").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "LLMProvider").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))

	ws.Route(ws.DELETE("/llm-providers/{id}").To(h.deleteLLMProvider).
		Doc("Delete LLM provider").
		Metadata(label.MetaIdentity, label.SysAccess).
		Metadata(label.MetaModule, label.SysModuleName).
		Metadata(label.MetaKind, "LLMProvider").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable))
}

// llmProviderView is the API-facing shape of a row.  It always carries
// api_key as the literal mask so the raw value never leaves the server.
type llmProviderView struct {
	model.LLMProvider
	APIKey string `json:"api_key"`
}

func toLLMView(p model.LLMProvider) llmProviderView {
	return llmProviderView{LLMProvider: p, APIKey: apiKeyMask}
}

// llmProviderInput is the inbound DTO for create/update.  We deliberately
// do NOT bind directly into model.LLMProvider because that struct's APIKey
// field is `json:"-"` (defense-in-depth so we never accidentally marshal
// ciphertext into a response).
//
// The api_key field accepts three shapes from the browser:
//
//	"ENC:<base64-rsa-ciphertext>"   – preferred, decrypted in-place
//	""        / "******"            – on update: keep existing ciphertext
//	"<plaintext>"                   – legacy / curl, accepted as-is
type llmProviderInput struct {
	Name                string  `json:"name"`
	Provider            string  `json:"provider"`
	BaseURL             string  `json:"base_url"`
	APIKey              string  `json:"api_key"`
	ModelName           string  `json:"model"`
	Temperature         float32 `json:"temperature"`
	IsDefault           bool    `json:"is_default"`
	IsEnabled           bool    `json:"is_enabled"`
	Language            string  `json:"language"`
	ChatReportMaxChars  int     `json:"chat_report_max_chars"`
	ChatHistoryMaxTurns int     `json:"chat_history_max_turns"`
}

// toModel materialises a model.LLMProvider, performing wire-decryption on
// api_key if the browser sent the ENC: prefix.  The returned APIKey is
// guaranteed to be plaintext (or empty) so callers can re-encrypt it with
// the AES-256-GCM at-rest key.
//
// Behaviour-knob fields (Language / ChatReport* / ChatHistory*) are passed
// through verbatim; agent.go applies the process-level fallbacks when a
// row carries 0 / "" (i.e. legacy rows from before migration 33).
func (in llmProviderInput) toModel() model.LLMProvider {
	return model.LLMProvider{
		Name:                strings.TrimSpace(in.Name),
		Provider:            strings.TrimSpace(in.Provider),
		BaseURL:             strings.TrimSpace(in.BaseURL),
		APIKey:              auth.DecodeClientCipher(in.APIKey),
		ModelName:           strings.TrimSpace(in.ModelName),
		Temperature:         in.Temperature,
		IsDefault:           in.IsDefault,
		IsEnabled:           in.IsEnabled,
		Language:            normalizeLanguage(in.Language),
		ChatReportMaxChars:  in.ChatReportMaxChars,
		ChatHistoryMaxTurns: in.ChatHistoryMaxTurns,
	}
}

// normalizeLanguage maps blank / unknown values to the safe default ("zh")
// so the CHECK constraint on llm_providers.language can never reject a
// well-meaning empty form submission.
func normalizeLanguage(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "en":
		return "en"
	case "auto":
		return "auto"
	default:
		return "zh"
	}
}

func (h *aiHandler) listLLMProviders(req *restful.Request, resp *restful.Response) {
	var rows []model.LLMProvider
	if err := h.db.WithContext(req.Request.Context()).
		Order("is_default desc, created_at desc").Find(&rows).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	out := make([]llmProviderView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toLLMView(r))
	}
	httputil.Success(resp, out)
}

func (h *aiHandler) createLLMProvider(req *restful.Request, resp *restful.Response) {
	var body llmProviderInput
	if err := req.ReadEntity(&body); err != nil {
		httputil.BadRequest(resp, "invalid request body")
		return
	}
	row := body.toModel()
	if msg := validateLLMProvider(row, true); msg != "" {
		httputil.BadRequest(resp, msg)
		return
	}

	enc, err := h.encryptAPIKey(row.APIKey)
	if err != nil {
		httputil.InternalError(resp, "encrypt api_key: "+err.Error())
		return
	}
	row.APIKey = enc
	row.ID = ""

	err = h.db.WithContext(req.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if row.IsDefault {
			if err := tx.Model(&model.LLMProvider{}).
				Where("is_default = ?", true).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Created(resp, toLLMView(row))
}

func (h *aiHandler) updateLLMProvider(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")

	var existing model.LLMProvider
	if err := h.db.WithContext(req.Request.Context()).First(&existing, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.NotFound(resp)
			return
		}
		httputil.InternalError(resp, err.Error())
		return
	}

	var body llmProviderInput
	if err := req.ReadEntity(&body); err != nil {
		httputil.BadRequest(resp, "invalid request body")
		return
	}
	row := body.toModel()
	if msg := validateLLMProvider(row, false); msg != "" {
		httputil.BadRequest(resp, msg)
		return
	}

	// api_key handling: empty / mask placeholder ⇒ keep existing ciphertext.
	// `row.APIKey` is already RSA-decrypted at this point (toModel ran the
	// ENC: decoder), so the comparison is against the plain mask sentinel.
	if row.APIKey == "" || row.APIKey == apiKeyMask {
		row.APIKey = existing.APIKey
	} else {
		enc, err := h.encryptAPIKey(row.APIKey)
		if err != nil {
			httputil.InternalError(resp, "encrypt api_key: "+err.Error())
			return
		}
		row.APIKey = enc
	}

	row.ID = id
	row.CreatedAt = existing.CreatedAt

	wantDefault := row.IsDefault

	err := h.db.WithContext(req.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if wantDefault && !existing.IsDefault {
			if err := tx.Model(&model.LLMProvider{}).
				Where("is_default = ? AND id <> ?", true, id).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Save(&row).Error
	})
	if err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, toLLMView(row))
}

func (h *aiHandler) setDefaultLLMProvider(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")

	var existing model.LLMProvider
	if err := h.db.WithContext(req.Request.Context()).First(&existing, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.NotFound(resp)
			return
		}
		httputil.InternalError(resp, err.Error())
		return
	}

	err := h.db.WithContext(req.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.LLMProvider{}).
			Where("is_default = ? AND id <> ?", true, id).
			Update("is_default", false).Error; err != nil {
			return err
		}
		return tx.Model(&model.LLMProvider{}).
			Where("id = ?", id).
			Updates(map[string]any{"is_default": true, "is_enabled": true}).Error
	})
	if err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, map[string]string{"id": id, "status": "default"})
}

func (h *aiHandler) deleteLLMProvider(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")
	if err := h.db.WithContext(req.Request.Context()).
		Delete(&model.LLMProvider{}, "id = ?", id).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, nil)
}

// testLLMProvider issues a 1-token "ping" generation to verify api_key /
// base_url / model are reachable.  When the request body provides an inline
// api_key (typical when the operator hasn't saved the row yet) we use it
// directly; otherwise we decrypt the stored ciphertext for the row at {id}.
func (h *aiHandler) testLLMProvider(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")

	type testReq struct {
		// Optional override fields; when empty the stored row is used as-is.
		BaseURL  string `json:"base_url"`
		Model    string `json:"model"`
		APIKey   string `json:"api_key"`
		Provider string `json:"provider"`
	}
	var body testReq
	_ = req.ReadEntity(&body)

	provider := model.LLMProvider{}
	if id != "" && id != "_" && id != "new" {
		if err := h.db.WithContext(req.Request.Context()).
			First(&provider, "id = ?", id).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				httputil.InternalError(resp, err.Error())
				return
			}
		}
	}

	if body.BaseURL != "" {
		provider.BaseURL = body.BaseURL
	}
	if body.Model != "" {
		provider.ModelName = body.Model
	}
	if body.Provider != "" {
		provider.Provider = body.Provider
	}

	// Browser may send the api_key as either plaintext or "ENC:<rsa-cipher>"
	// (see web/src/api/crypto.ts).  DecodeClientCipher returns the value
	// unchanged when the prefix isn't present, so this is a no-op for
	// legacy / curl callers.
	apiKey := auth.DecodeClientCipher(body.APIKey)
	if apiKey == "" || apiKey == apiKeyMask {
		decrypted, err := h.decryptAPIKey(provider.APIKey)
		if err != nil {
			httputil.BadRequest(resp, "no usable api_key (provide one in the body or save the row first)")
			return
		}
		apiKey = decrypted
	}
	if apiKey == "" {
		httputil.BadRequest(resp, "api_key is required")
		return
	}
	if provider.ModelName == "" {
		httputil.BadRequest(resp, "model is required")
		return
	}

	var llm llms.Model
	var err error
	switch provider.Provider {
	case "qoder":
		qoderCtx, cancel := context.WithTimeout(req.Request.Context(), 90*time.Second)
		defer cancel()
		sample, err := ai.TestQoderProvider(qoderCtx, provider.BaseURL, apiKey, provider.ModelName)
		if err != nil {
			httputil.Error(resp, http.StatusBadGateway, "test call failed: "+err.Error())
			return
		}
		httputil.Success(resp, map[string]any{
			"ok":     true,
			"model":  provider.ModelName,
			"sample": sample,
		})
		return
	case "anthropic":
		// Use a direct HTTP call instead of langchaingo so we can skip
		// "thinking" content blocks that some Anthropic-compatible proxies
		// inject even for non-thinking model names.
		anthropicCtx, cancel := context.WithTimeout(req.Request.Context(), 30*time.Second)
		defer cancel()
		sample, err := testAnthropicDirect(anthropicCtx, provider.BaseURL, apiKey, provider.ModelName)
		if err != nil {
			httputil.Error(resp, http.StatusBadGateway, "test call failed: "+err.Error())
			return
		}
		httputil.Success(resp, map[string]any{
			"ok":     true,
			"model":  provider.ModelName,
			"sample": sample,
		})
		return
	default:
		llmOpts := []openai.Option{
			openai.WithToken(apiKey),
			openai.WithModel(provider.ModelName),
		}
		if provider.BaseURL != "" {
			llmOpts = append(llmOpts, openai.WithBaseURL(provider.BaseURL))
		}
		llm, err = openai.New(llmOpts...)
		if err != nil {
			httputil.Error(resp, http.StatusBadGateway, "init openai LLM client: "+err.Error())
			return
		}
	}

	ctx, cancel := context.WithTimeout(req.Request.Context(), 15*time.Second)
	defer cancel()

	completion, err := llm.GenerateContent(ctx,
		[]llms.MessageContent{
			llms.TextParts(llms.ChatMessageTypeHuman, "ping"),
		},
		llms.WithMaxTokens(8),
		llms.WithTemperature(0),
	)
	if err != nil {
		httputil.Error(resp, http.StatusBadGateway, "test call failed: "+err.Error())
		return
	}

	var sample string
	if len(completion.Choices) > 0 {
		sample = completion.Choices[0].Content
	}
	httputil.Success(resp, map[string]any{
		"ok":     true,
		"model":  provider.ModelName,
		"sample": strings.TrimSpace(sample),
	})
}

// validateLLMProvider returns "" on success or a human-readable error.  The
// `creating` flag relaxes the api_key requirement for updates (operators can
// edit metadata without re-entering the secret).
//
// Behaviour-knob bounds are enforced here (not in the DB) so the operator
// gets a friendly 400 instead of a Postgres CHECK constraint error blob.
// The upper bounds are deliberately generous — they're only there to catch
// obvious mistakes (e.g. typing 800000 instead of 8000) that would explode
// the LLM context.
func validateLLMProvider(p model.LLMProvider, creating bool) string {
	if strings.TrimSpace(p.Name) == "" {
		return "name is required"
	}
	if strings.TrimSpace(p.Provider) == "" {
		return "provider is required"
	}
	if strings.TrimSpace(p.ModelName) == "" {
		return "model is required"
	}
	if creating && strings.TrimSpace(p.APIKey) == "" {
		return "api_key is required"
	}
	switch p.Language {
	case "", "zh", "en", "auto":
		// ok ("" is treated as zh by toModel via normalizeLanguage)
	default:
		return "language must be one of: zh, en, auto"
	}
	if p.ChatReportMaxChars < 0 || p.ChatReportMaxChars > 200000 {
		return "chat_report_max_chars must be between 0 and 200000 (0 = use default)"
	}
	if p.ChatHistoryMaxTurns < 0 || p.ChatHistoryMaxTurns > 200 {
		return "chat_history_max_turns must be between 0 and 200 (0 = use default)"
	}
	return ""
}

// encryptAPIKey wraps cfgcrypto.Encrypt so that an empty EncryptionKey
// (development setups) round-trips the value as plaintext, matching the
// fallback in `internal/ai/agent.go::decryptAPIKey`.
func (h *aiHandler) encryptAPIKey(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if h.cfg == nil || h.cfg.EncryptionKey == "" {
		return plain, nil
	}
	enc, err := cfgcrypto.Encrypt(plain, h.cfg.EncryptionKey)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}
	return enc, nil
}

// decryptAPIKey is the read-side counterpart used by the test endpoint.
// It mirrors `internal/ai/agent.go::decryptAPIKey`: a decryption failure is
// treated as "value is already plaintext" so old SQL-seeded rows still work.
func (h *aiHandler) decryptAPIKey(stored string) (string, error) { //nolint:unparam // error reserved for future strict-decrypt mode
	if stored == "" {
		return "", nil
	}
	if h.cfg == nil || h.cfg.EncryptionKey == "" {
		return stored, nil
	}
	plain, err := cfgcrypto.Decrypt(stored, h.cfg.EncryptionKey)
	if err != nil {
		return stored, nil //nolint:nilerr
	}
	return plain, nil
}

// testAnthropicDirect calls the Anthropic Messages API directly, bypassing
// langchaingo so that "thinking" content blocks returned by some proxies can
// be ignored. The request is streamed so that we can return as soon as the
// first text tokens arrive, instead of waiting for a long thinking block to
// finish in non-streaming mode.
func testAnthropicDirect(ctx context.Context, baseURL, apiKey, modelName string) (string, error) {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	} else {
		baseURL = strings.TrimRight(baseURL, "/")
		if !strings.HasSuffix(baseURL, "/v1") {
			baseURL += "/v1"
		}
	}

	body, _ := json.Marshal(map[string]interface{}{
		"model":      modelName,
		"max_tokens": 1024,
		"stream":     true,
		"messages": []map[string]interface{}{
			{"role": "user", "content": "ping"},
		},
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(b))
	}

	scanner := bufio.NewScanner(resp.Body)
	var sample strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		var data string
		if strings.HasPrefix(line, "data: ") {
			data = line[6:]
		} else if strings.HasPrefix(line, "data:") {
			data = line[5:]
		} else {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
			sample.WriteString(event.Delta.Text)
			if sample.Len() >= 20 {
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read stream: %w", err)
	}
	return strings.TrimSpace(sample.String()), nil
}
