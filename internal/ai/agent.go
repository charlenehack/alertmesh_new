package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/callbacks"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/llms/openai"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/tools"
	"gorm.io/gorm"

	aitools "github.com/kuzane/alertmesh/internal/ai/tools"
	"github.com/kuzane/alertmesh/internal/config"
	"github.com/kuzane/alertmesh/internal/model"
)

// Process-level fallbacks for the per-provider chat-context caps.  These
// kick in whenever model.LLMProvider.ChatReportMaxChars / ChatHistoryMaxTurns
// are 0 (legacy rows seeded before migration 33, or rows that the operator
// explicitly left blank in the UI to mean "use the system default").
//
// Both are character (rune-byte) caps applied AFTER tool reasoning — they
// only bound what we *re-feed* into the chat agent on follow-up turns;
// live tool output during analysis is never truncated.
const (
	defaultChatReportMaxChars  = 8000 // ~2k tokens (CN) / ~2k tokens (EN)
	defaultChatHistoryMaxTurns = 10   // last N user/assistant pairs
)

// resolveChatLimits picks the effective values for the two chat-context caps,
// preferring per-provider settings over the process-level fallbacks.
// A negative or zero value in the DB row means "use the default".
func resolveChatLimits(p model.LLMProvider) (reportMax, historyMax int) {
	reportMax = p.ChatReportMaxChars
	if reportMax <= 0 {
		reportMax = defaultChatReportMaxChars
	}
	historyMax = p.ChatHistoryMaxTurns
	if historyMax <= 0 {
		historyMax = defaultChatHistoryMaxTurns
	}
	return reportMax, historyMax
}

// resolveLanguage normalises the provider language to one of "zh" / "en" /
// "auto", falling back to "zh" for unknown / blank values so the prompt
// builders never have to handle a fourth case.
func resolveLanguage(p model.LLMProvider) string {
	switch strings.ToLower(strings.TrimSpace(p.Language)) {
	case "en":
		return "en"
	case "auto":
		return "auto"
	default:
		return "zh"
	}
}

// Agent wraps a langchaingo ReAct agent with the configured tools and LLM provider.
type Agent struct {
	db  *gorm.DB
	cfg *config.Config
}

func NewAgent(db *gorm.DB, cfg *config.Config) *Agent {
	return &Agent{db: db, cfg: cfg}
}

// Analyze runs the ReAct loop for the given incident and returns a Markdown report.
func (a *Agent) Analyze(ctx context.Context, incidentID string, cb callbacks.Handler) (string, error) {
	var inc model.Incident
	if err := a.db.WithContext(ctx).Preload("Alerts").First(&inc, "id = ?", incidentID).Error; err != nil {
		return "", fmt.Errorf("load incident: %w", err)
	}

	var provider model.LLMProvider
	if err := a.db.WithContext(ctx).Where("is_default = ? AND is_enabled = ?", true, true).First(&provider).Error; err != nil {
		return "", fmt.Errorf("no default LLM provider configured: %w", err)
	}

	apiKey, err := a.decryptAPIKey(provider.APIKey)
	if err != nil {
		return "", fmt.Errorf("decrypt LLM API key: %w", err)
	}

	llm, err := newLLMFromProvider(provider, apiKey, cb)
	if err != nil {
		return "", fmt.Errorf("create LLM client: %w", err)
	}

	agentTools := aitools.AsLangchainTools(aitools.ToolConfig{
		PrometheusURL: a.cfg.PrometheusURL,
		OpenSearchURL: a.cfg.OpenSearchURL,
	})

	language := resolveLanguage(provider)
	prompt := buildAnalysisPrompt(inc, language)

	// Anthropic models (especially Claude 3.7 with thinking blocks) trip up
	// langchaingo's SSE/agent parsers. Use a direct Messages API ReAct loop.
	if provider.Provider == "anthropic" {
		log.Info().
			Str("incident_id", incidentID).
			Str("provider", provider.Provider).
			Str("model", provider.ModelName).
			Str("language", language).
			Msg("starting AI analysis via direct Anthropic ReAct loop")
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		return runAnthropicReAct(ctx, apiKey, provider.BaseURL, provider.ModelName,
			anthropicAnalysisSystemPrompt(language), prompt, agentTools, 10, cb)
	}

	// langchaingo marks Initialize "deprecated" but the only suggested
	// replacement (NewExecutor) lacks the higher-level option helpers we
	// rely on here; keeping Initialize keeps the call site terse.
	agent, err := agents.Initialize( //nolint:staticcheck // langchaingo deprecation, see comment above
		llm,
		agentTools,
		agents.ZeroShotReactDescription,
		agents.WithMaxIterations(10),
	)
	if err != nil {
		return "", fmt.Errorf("initialize agent: %w", err)
	}

	log.Info().
		Str("incident_id", incidentID).
		Str("provider", provider.Provider).
		Str("model", provider.ModelName).
		Str("language", language).
		Msg("starting AI analysis")

	result, err := agent.Call(ctx, map[string]any{
		"input": prompt,
	})
	if err != nil {
		return "", fmt.Errorf("agent execution: %w", err)
	}

	output, ok := result["output"].(string)
	if !ok {
		return "", fmt.Errorf("unexpected agent output type")
	}

	return output, nil
}

// Chat handles a follow-up conversation turn for an incident.
//
// Unlike the old version, this preloads the full conversational context so
// the agent can give a meaningful answer even when the user's question is
// terse (e.g. "中文输出" / "再详细点" / "上一条 metric 怎么查的"):
//
//   - the incident itself (title / severity / first few alert labels)
//   - the most recent root-cause report from ai_analyses (if any)
//   - the recent ai_conversations turns
//
// Without these, the conversational agent only sees the bare question and
// degrades into "what do you need help with?" replies.
func (a *Agent) Chat(ctx context.Context, incidentID, question string, history []model.AIConversation, cb callbacks.Handler) (string, error) {
	var inc model.Incident
	if err := a.db.WithContext(ctx).Preload("Alerts").First(&inc, "id = ?", incidentID).Error; err != nil {
		return "", fmt.Errorf("load incident: %w", err)
	}

	// Latest analysis is the canonical "what we already concluded" anchor.
	// A miss is fine — the chat can still run, the prompt builder will
	// switch to a "no prior report" template.
	var analysis model.AIAnalysis
	if err := a.db.WithContext(ctx).
		Where("incident_id = ?", incidentID).
		Order("created_at DESC").
		First(&analysis).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("load latest AI analysis: %w", err)
	}

	var provider model.LLMProvider
	if err := a.db.WithContext(ctx).Where("is_default = ? AND is_enabled = ?", true, true).First(&provider).Error; err != nil {
		return "", fmt.Errorf("no default LLM provider configured: %w", err)
	}

	apiKey, err := a.decryptAPIKey(provider.APIKey)
	if err != nil {
		return "", fmt.Errorf("decrypt LLM API key: %w", err)
	}

	llm, err := newLLMFromProvider(provider, apiKey, cb)
	if err != nil {
		return "", fmt.Errorf("create LLM client: %w", err)
	}

	agentTools := aitools.AsLangchainTools(aitools.ToolConfig{
		PrometheusURL: a.cfg.PrometheusURL,
		OpenSearchURL: a.cfg.OpenSearchURL,
	})

	reportMax, historyMax := resolveChatLimits(provider)
	language := resolveLanguage(provider)
	prompt := buildChatPrompt(inc, analysis.Report, history, question, reportMax, historyMax, language)

	// Anthropic models (especially Claude 3.7 with thinking blocks) trip up
	// langchaingo's SSE/agent parsers. Use a direct Messages API ReAct loop.
	if provider.Provider == "anthropic" {
		log.Debug().
			Str("incident_id", incidentID).
			Str("provider", provider.Provider).
			Str("model", provider.ModelName).
			Str("language", language).
			Msg("starting AI chat via direct Anthropic ReAct loop")
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		return runAnthropicReAct(ctx, apiKey, provider.BaseURL, provider.ModelName,
			anthropicChatSystemPrompt(language), prompt, agentTools, 8, cb)
	}

	agent, err := agents.Initialize( //nolint:staticcheck // langchaingo deprecation, see ZeroShot path comment
		llm,
		agentTools,
		agents.ConversationalReactDescription,
		agents.WithMaxIterations(8),
	)
	if err != nil {
		return "", fmt.Errorf("initialize conversational agent: %w", err)
	}

	log.Debug().
		Str("incident_id", incidentID).
		Bool("has_prior_report", analysis.Report != "").
		Int("history_turns", len(history)).
		Int("prompt_chars", len(prompt)).
		Int("report_max_chars", reportMax).
		Int("history_max_turns", historyMax).
		Str("language", language).
		Msg("AI chat prompt built")

	result, err := agent.Call(ctx, map[string]any{
		"input": prompt,
	})
	if err != nil {
		return "", fmt.Errorf("agent chat: %w", err)
	}

	output, ok := result["output"].(string)
	if !ok {
		return "", fmt.Errorf("unexpected agent output type")
	}

	return output, nil
}

// decryptAPIKey returns (key, nil) today: a decrypt failure means the
// row was stored in plaintext (development), in which case we return
// the input verbatim.  The error return is kept for forward-compat so
// future strict modes can refuse plaintext fallbacks without a signature
// change at every call site.
func (a *Agent) decryptAPIKey(encrypted string) (string, error) { //nolint:unparam // see comment above
	if a.cfg.EncryptionKey == "" {
		return encrypted, nil
	}
	decrypted, err := config.Decrypt(encrypted, a.cfg.EncryptionKey)
	if err != nil {
		// If decryption fails, it might be stored in plaintext (e.g. during development)
		return encrypted, nil //nolint:nilerr
	}
	return decrypted, nil
}

// newLLMFromProvider creates the appropriate langchaingo LLM based on provider kind.
func newLLMFromProvider(provider model.LLMProvider, apiKey string, cb callbacks.Handler) (llms.Model, error) {
	switch provider.Provider {
	case "anthropic":
		baseURL := provider.BaseURL
		if baseURL != "" && !strings.HasSuffix(baseURL, "/v1") {
			baseURL = strings.TrimSuffix(baseURL, "/") + "/v1"
		}
		llm, err := anthropic.New(
			anthropic.WithToken(apiKey),
			anthropic.WithModel(provider.ModelName),
			anthropic.WithBaseURL(baseURL),
		)
		if err != nil {
			return nil, fmt.Errorf("create anthropic LLM: %w", err)
		}
		if cb != nil {
			llm.CallbacksHandler = cb
		}
		return llm, nil
	default:
		opts := []openai.Option{
			openai.WithToken(apiKey),
			openai.WithModel(provider.ModelName),
		}
		if provider.BaseURL != "" {
			opts = append(opts, openai.WithBaseURL(provider.BaseURL))
		}
		if cb != nil {
			opts = append(opts, openai.WithCallback(cb))
		}
		llm, err := openai.New(opts...)
		if err != nil {
			return nil, fmt.Errorf("create openai LLM: %w", err)
		}
		return llm, nil
	}
}

// maxAlertDetailsRunes caps the Alert Details section in the analysis prompt
// so very large Kafka groups stay within a reasonable context window.
const maxAlertDetailsRunes = 45000

// buildAlertDetailsBlock renders the data-only block that's identical
// across all output languages.  Field labels stay English (`source=`,
// `status=`, `labels=`) so the model gets a stable, machine-friendly
// snapshot regardless of UI language.
//
// Alerts are ordered by StartsAt and included until maxAlertDetailsRunes
// runes are reached (summary + description when present).
func buildAlertDetailsBlock(inc model.Incident) string {
	alerts := append([]model.Alert(nil), inc.Alerts...)
	sort.Slice(alerts, func(i, j int) bool {
		return alerts[i].StartsAt.Before(alerts[j].StartsAt)
	})
	if len(alerts) == 0 {
		return "\n(no alerts attached)"
	}

	var b strings.Builder
	total := 0
	for i, alert := range alerts {
		var labels map[string]string
		_ = json.Unmarshal(alert.Labels, &labels)
		var annotations map[string]string
		_ = json.Unmarshal(alert.Annotations, &annotations)

		var chunk strings.Builder
		fmt.Fprintf(&chunk,
			"\n- Alert %d: source=%s fingerprint=%s status=%s starts_at=%s labels=%v",
			i+1, alert.Source, alert.Fingerprint, alert.Status, alert.StartsAt.UTC().Format(time.RFC3339), labels,
		)
		if sum := annotations["summary"]; sum != "" {
			fmt.Fprintf(&chunk, "\n  Summary: %s", sum)
		}
		if desc := annotations["description"]; desc != "" {
			fmt.Fprintf(&chunk, "\n  Description: %s", desc)
		}

		s := chunk.String()
		rs := []rune(s)
		n := len(rs)
		if total+n > maxAlertDetailsRunes {
			if remain := maxAlertDetailsRunes - total; remain > 0 {
				b.WriteString(string(rs[:remain]))
			}
			fmt.Fprintf(&b, "\n... (truncated: %d alerts total, cap %d runes)", len(alerts), maxAlertDetailsRunes)
			break
		}
		b.WriteString(s)
		total += n
	}
	return b.String()
}

// analysisTimeAnchorBlock gives the model explicit UTC timestamps so it can
// size Prometheus ranges and pick a large enough logs_search time_range.
// logs_search still filters @timestamp to [now−time_range, now] at query time,
// so the hint stresses choosing a duration that still covers the incident.
func analysisTimeAnchorBlock(inc model.Incident) string {
	opened := inc.OpenedAt.UTC().Format(time.RFC3339)
	if len(inc.Alerts) == 0 {
		return fmt.Sprintf(`## Time anchor (UTC) — use for metrics_query / logs_search windowing
- **Incident opened_at:** %s
- **Attached alerts:** none

**Important:** `+"`logs_search`"+` filters `+"`@timestamp`"+` to [**now − time_range**, **now**]. Pick `+"`time_range`"+` large enough that this incident still falls inside that wall-clock window.`,
			opened)
	}
	minT := inc.Alerts[0].StartsAt
	maxT := inc.Alerts[0].StartsAt
	for _, a := range inc.Alerts[1:] {
		if a.StartsAt.Before(minT) {
			minT = a.StartsAt
		}
		if a.StartsAt.After(maxT) {
			maxT = a.StartsAt
		}
	}
	minS := minT.UTC().Format(time.RFC3339)
	maxS := maxT.UTC().Format(time.RFC3339)
	return fmt.Sprintf(`## Time anchor (UTC) — use for metrics_query / logs_search windowing
- **Incident opened_at:** %s
- **Alert starts_at range (min … max):** %s … %s

**Important:** `+"`logs_search`"+` filters `+"`@timestamp`"+` to [**now − time_range**, **now**]. Choose `+"`time_range`"+` at least long enough to cover **now − min(incident times above)** with margin (e.g. if the earliest signal was 2h ago, prefer `+"`3h`"+` or `+"`6h`"+` over `+"`30m`"+`). For **metrics_query**, prefer range vectors or subqueries overlapping this window.`,
		opened, minS, maxS)
}

// buildAnalysisPrompt produces the system prompt fed to the ZeroShotReact
// agent that generates the root-cause report.
//
// `language` is one of "zh" / "en" / "auto" (resolveLanguage normalises any
// other value to "zh"):
//
//   - zh: 中文 prompt + 中文 section headings + 强制中文输出
//   - en: English prompt + English section headings + "Reply in English"
//   - auto: English skeleton + "Reply in the same language as the incident
//     text and alert annotations" (so a Chinese-labelled incident gets a
//     Chinese report and an English one gets an English report)
//
// IMPORTANT: ZeroShotReactDescription parses the model's output looking for
// the literal English keywords `Thought:` / `Action:` / `Action Input:` /
// `Observation:` / `Final Answer:`.  Those MUST stay English regardless of
// the chosen language, which is why each branch repeats the same warning.
func buildAnalysisPrompt(inc model.Incident, language string) string {
	alertDetails := buildAlertDetailsBlock(inc)
	timeAnchor := analysisTimeAnchorBlock(inc)

	switch language {
	case "en":
		return fmt.Sprintf(`You are a senior SRE performing root cause analysis on a production incident.

## Incident Details
- **Title:** %s
- **Severity:** %s
- **Status:** %s
- **Alert Source:** %s
- **Alert Count:** %d

%s

## Alert Details
%s

## Investigation Order (call the tools in this order)
1. **metrics_query** — PromQL for resources (CPU/mem/disk/net), error rate, latency percentiles, saturation.
2. **logs_search** — application logs in OpenSearch around the incident window (ERROR / Exception / Stack trace).
3. **system_info** — host-level logs (kernel / disk / network / OOM / systemd service) for the affected hosts.
4. **changes_query** — recent deployments / config changes correlated with the incident time.
5. **runbook_search** — existing runbooks / SOPs that suggest remediation.

## Output Requirements (strict)
- Keep the ReAct keywords (`+"`Thought` / `Action` / `Action Input` / `Observation` / `Final Answer`"+`) in **English** — they are protocol tokens.
- The content after `+"`Final Answer`"+` MUST be written in **English**.
- Keep service names / metric names / paths / commands / exception class names in their original form (do not translate).
- Output as Markdown, with EXACTLY these section headings:

  ### Root Cause Analysis
  Causal chain from trigger to user-visible symptom.

  ### Evidence
  Key metric values (with timestamps and units) and representative log lines that justify the conclusion.

  ### Impact Assessment
  Services / hosts / users affected and the business impact.

  ### Remediation Steps
  Immediate, executable actions (commands / steps) to recover.

  ### Prevention
  Long-term fixes to prevent recurrence.

Be concise but thorough; favour actionable findings over generalities.`,
			inc.Title, inc.Severity, inc.Status, inc.Source, len(inc.Alerts), timeAnchor, alertDetails)

	case "auto":
		return fmt.Sprintf(`You are a senior SRE performing root cause analysis on a production incident.

## Incident Details
- **Title:** %s
- **Severity:** %s
- **Status:** %s
- **Alert Source:** %s
- **Alert Count:** %d

%s

## Alert Details
%s

## Investigation Order
1. metrics_query  2. logs_search  3. system_info  4. changes_query  5. runbook_search

## Output Requirements (strict)
- Keep the ReAct keywords (`+"`Thought` / `Action` / `Action Input` / `Observation` / `Final Answer`"+`) in **English** — they are protocol tokens.
- For the content after `+"`Final Answer`"+`: **reply in the same language used in the incident title / alert labels / annotations above.**  If those texts are Chinese, reply in 简体中文; if English, reply in English; if mixed, follow the language of the incident title.
- Keep service names / metric names / paths / commands / exception class names in their original form.
- Output as Markdown with five sections:
  Root Cause Analysis / Evidence / Impact Assessment / Remediation Steps / Prevention
  (use the localised heading appropriate to the chosen language).

Be concise but thorough; favour actionable findings.`,
			inc.Title, inc.Severity, inc.Status, inc.Source, len(inc.Alerts), timeAnchor, alertDetails)
	}

	// "zh" (default).  ZeroShotReactDescription requires English keywords
	// ("Thought:" / "Action:" / "Observation:" / "Final Answer:") — those
	// MUST stay English or langchaingo's parser breaks.  We only constrain
	// the *Final Answer* content to Chinese.
	return fmt.Sprintf(`你是一名资深 SRE，正在为一起线上故障执行根因分析。

## 故障详情
- **标题：** %s
- **严重等级：** %s
- **状态：** %s
- **告警来源：** %s
- **告警数量：** %d

%s

## 告警明细
%s

## 调查指引（按顺序使用工具）
1. **metrics_query** — 通过 PromQL 查询资源指标（CPU、内存、磁盘、网络）、错误率、延迟分位数、饱和度信号。
2. **logs_search** — 在 OpenSearch 检索故障时间窗口内的应用日志，关注 ERROR / Exception / Stack trace。
3. **system_info** — 查看受影响主机的系统级日志（kernel、disk、network、OOM、systemd service）。
4. **changes_query** — 检索发布 / 配置变更，判断是否与故障时间相关。
5. **runbook_search** — 查找已有 runbook / SOP，给出处置建议。

## 输出要求（强制）
- 工具调用时的 `+"`Thought` / `Action` / `Action Input` / `Observation`"+` 关键字保持英文（这是 ReAct 协议的一部分）。
- `+"`Final Answer`"+` 之后的报告内容**必须使用简体中文**。
- 技术术语 / 服务名 / metric 名 / 路径 / 命令 / 异常类名保留原文，方便直接复制使用。
- 报告以 Markdown 输出，必须包含以下章节，**严格使用以下中文标题**：

  ### 根因分析
  描述从触发因素到症状的完整因果链。

  ### 证据
  关键指标值（带时间戳与单位）与代表性的日志片段，每条要能复现本次结论。

  ### 影响评估
  受影响的服务 / 主机 / 用户范围与业务后果。

  ### 应急处置
  当前可立即执行的修复动作（命令 / 操作步骤）。

  ### 长期改进
  防止同类问题复发的根治措施。

请保持简洁但充分，避免空话，结论要落到具体动作上。`,
		inc.Title, inc.Severity, inc.Status, inc.Source, len(inc.Alerts), timeAnchor, alertDetails)
}

// buildChatPrompt assembles the system context fed into the conversational
// agent for a follow-up turn.
//
// Caller-supplied knobs (all per-LLM-provider, see resolveChatLimits /
// resolveLanguage):
//
//   - reportMax  – cap on prior-report chars re-fed into the chat
//   - historyMax – cap on prior conversation pairs re-fed
//   - language   – "zh" / "en" / "auto"; controls the persona + section
//     copy. ReAct keywords stay English in every branch.
//
// Layout (every branch):
//
//  1. Persona + language directive
//  2. Incident snapshot (so the model knows *which* outage)
//  3. Prior root-cause report (capped + truncated)
//  4. Recent conversation turns (capped)
//  5. The current question
func buildChatPrompt(
	inc model.Incident,
	analysisReport string,
	history []model.AIConversation,
	question string,
	reportMax, historyMax int,
	language string,
) string {
	if reportMax <= 0 {
		reportMax = defaultChatReportMaxChars
	}
	if historyMax <= 0 {
		historyMax = defaultChatHistoryMaxTurns
	}

	// 1) language-neutral alert snapshot – field labels stay English so
	//    the data shape is stable across UIs.
	var alertSummary strings.Builder
	for i, alert := range inc.Alerts {
		if i >= 3 {
			fmt.Fprintf(&alertSummary, "\n  - … +%d more alerts", len(inc.Alerts)-3)
			break
		}
		var labels map[string]string
		_ = json.Unmarshal(alert.Labels, &labels)
		fmt.Fprintf(&alertSummary,
			"\n  - source=%s status=%s labels=%v",
			alert.Source, alert.Status, labels,
		)
	}

	// 2) recent chat turns – drop everything except the last N exchanges.
	//    We exclude the *current* question since the agent's input field
	//    already carries it; including it twice tempts the model to echo.
	turns := history
	if maxTurns := historyMax * 2; len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
	}
	roleAI, roleUser, historyHeader := chatRoleLabels(language)
	var historyBlock strings.Builder
	for _, msg := range turns {
		if msg.Role == "user" && strings.TrimSpace(msg.Content) == strings.TrimSpace(question) {
			continue
		}
		role := msg.Role
		switch role {
		case "assistant":
			role = roleAI
		case "user":
			role = roleUser
		}
		fmt.Fprintf(&historyBlock, "- %s: %s\n", role, msg.Content)
	}
	var historyText string
	if historyBlock.Len() > 0 {
		historyText = historyHeader + historyBlock.String() + "\n"
	}

	// 3) prior analysis report – the single most important block; without
	//    it the model regresses to generic "what do you need" replies.
	reportBlock := chatReportBlock(analysisReport, reportMax, language)

	// 4) full prompt – branch on language for persona / labels / question
	//    header.  ReAct keywords stay English in every branch.
	switch language {
	case "en":
		return fmt.Sprintf(`You are a senior SRE in a follow-up chat about a production incident.
**Always reply in English** (keep service names / metric names / paths / commands / exception class names in their original form).
Style: direct, evidence-based, actionable.  Call tools when you need more data, but do NOT paste back the entire prior report.

Note: keep the ReAct keywords (`+"`Thought` / `Action` / `Action Input` / `Observation`"+`) in English — they are protocol tokens.  Only the content after `+"`Final Answer`"+` is the user-facing reply.

## Incident
- title: %s
- severity: %s
- status: %s
- alert count: %d
- alert summary: %s

%s%s## Current question
%s

Answer based on the context above.`,
			inc.Title, inc.Severity, inc.Status, len(inc.Alerts), alertSummary.String(),
			reportBlock, historyText, question)

	case "auto":
		return fmt.Sprintf(`You are a senior SRE in a follow-up chat about a production incident.
**Reply in the same language the user used in the current question**, or in the language of the prior report if the question is too short to detect.  Keep service names / metric names / paths / commands / exception class names in their original form.
Style: direct, evidence-based, actionable.  Call tools for new evidence, do NOT re-paste the entire prior report.

Note: keep the ReAct keywords (`+"`Thought` / `Action` / `Action Input` / `Observation`"+`) in English — they are protocol tokens.  Only the content after `+"`Final Answer`"+` is the user-facing reply.

## Incident
- title: %s
- severity: %s
- status: %s
- alert count: %d
- alert summary: %s

%s%s## Current question
%s

Answer based on the context above.`,
			inc.Title, inc.Severity, inc.Status, len(inc.Alerts), alertSummary.String(),
			reportBlock, historyText, question)
	}

	// "zh" (default)
	return fmt.Sprintf(`你是一名资深 SRE，正在与运维同事就一起线上故障进行追问对话。
**请始终使用简体中文回答**（技术术语 / 服务名 / metric 名 / 路径 / 命令 / 异常类名保留原文）。
回答风格：直接、有依据、可落地，不要客套。需要进一步证据时主动调用工具，但不要重复贴一遍上一轮的整份报告。

注意：工具调用阶段的 `+"`Thought` / `Action` / `Action Input` / `Observation`"+` 关键字必须保持英文（ReAct 协议要求），仅 `+"`Final Answer`"+` 之后的内容用中文。

## 故障基本信息
- 标题：%s
- 严重等级：%s
- 状态：%s
- 告警数量：%d
- 告警摘要：%s

%s%s## 当前追问
%s

请基于上述上下文回答。`,
		inc.Title, inc.Severity, inc.Status, len(inc.Alerts), alertSummary.String(),
		reportBlock, historyText, question)
}

// chatReportBlock formats the prior root-cause report block for the chat
// prompt, applying the per-provider char cap and emitting a localized
// heading + truncation note.
func chatReportBlock(report string, maxLen int, language string) string {
	switch language {
	case "en":
		switch {
		case report == "":
			return "(No prior AI root-cause report available — please investigate via tools and answer.)\n"
		case len(report) > maxLen:
			return fmt.Sprintf(
				"## Prior AI root-cause report (truncated; do NOT paste it back wholesale)\n\n%s\n…(report truncated; query tools for more detail if needed)\n",
				report[:maxLen],
			)
		default:
			return fmt.Sprintf(
				"## Prior AI root-cause report (do NOT paste it back wholesale)\n\n%s\n",
				report,
			)
		}
	case "auto":
		switch {
		case report == "":
			return "(No prior AI root-cause report available — investigate via tools and answer.)\n"
		case len(report) > maxLen:
			return fmt.Sprintf(
				"## Prior AI root-cause report (truncated)\n\n%s\n…(truncated)\n",
				report[:maxLen],
			)
		default:
			return fmt.Sprintf("## Prior AI root-cause report\n\n%s\n", report)
		}
	}
	// "zh"
	switch {
	case report == "":
		return "（尚未生成 AI 根因分析报告 —— 请先用工具自行调查再回答。）\n"
	case len(report) > maxLen:
		return fmt.Sprintf(
			"## 上一轮 AI 根因分析报告（已截断，请基于此回答追问，不要重复整篇报告）\n\n%s\n…（报告过长，已截断，必要时请用工具补充查询）\n",
			report[:maxLen],
		)
	default:
		return fmt.Sprintf(
			"## 上一轮 AI 根因分析报告（请基于此回答追问，不要重复整篇报告）\n\n%s\n",
			report,
		)
	}
}

// chatRoleLabels returns the localized role labels and section header used
// when re-feeding prior conversation turns into a follow-up chat prompt.
// roleAI is currently always "AI" but we keep the named return so future
// localizations (e.g. "助手") only need to touch this file, not callers.
func chatRoleLabels(language string) (roleAI, roleUser, header string) { //nolint:unparam // see comment above
	switch language {
	case "en":
		return "AI", "User", "## Prior chat turns\n"
	case "auto":
		return "AI", "User", "## Prior chat turns\n"
	}
	return "AI", "运维", "## 之前的追问对话\n"
}

// K8sAnalysisKind distinguishes which K8s artefact is being analysed.
type K8sAnalysisKind string

const (
	K8sAnalysisLogs   K8sAnalysisKind = "logs"
	K8sAnalysisEvents K8sAnalysisKind = "events"
)

// AnalyzeK8sRequest carries the metadata and raw text for a K8s AI analysis.
type AnalyzeK8sRequest struct {
	ResourceKind string // Pod / Deployment / DaemonSet
	Namespace    string
	Name         string
	AnalysisKind K8sAnalysisKind // "logs" or "events"
	Content      string          // raw log lines or JSON-formatted event list
}

// AnalyzeK8s calls the default LLM provider and streams the response token by
// token via the onToken callback.  The caller is responsible for flushing the
// HTTP response; this function blocks until the stream is complete or ctx is
// cancelled.
func (a *Agent) AnalyzeK8s(ctx context.Context, req AnalyzeK8sRequest, onToken func(string)) error {
	var provider model.LLMProvider
	if err := a.db.WithContext(ctx).Where("is_default = ? AND is_enabled = ?", true, true).First(&provider).Error; err != nil {
		return fmt.Errorf("no default LLM provider configured: %w", err)
	}

	apiKey, err := a.decryptAPIKey(provider.APIKey)
	if err != nil {
		return fmt.Errorf("decrypt LLM API key: %w", err)
	}

	language := resolveLanguage(provider)
	prompt := buildK8sAnalysisPrompt(req, language)

	log.Info().
		Str("resource", req.ResourceKind+"/"+req.Namespace+"/"+req.Name).
		Str("kind", string(req.AnalysisKind)).
		Msg("starting K8s AI analysis")

	// Use direct HTTP streaming for Anthropic to avoid langchaingo SSE parsing bugs.
	// For other providers fall back to langchaingo.
	if provider.Provider == "anthropic" {
		return streamAnthropicDirect(ctx, apiKey, provider.BaseURL, provider.ModelName, prompt, onToken)
	}

	llmModel, err := newLLMFromProvider(provider, apiKey, nil)
	if err != nil {
		return fmt.Errorf("create LLM client: %w", err)
	}
	_, err = llmModel.GenerateContent(ctx,
		[]llms.MessageContent{
			{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart(prompt)}},
		},
		llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
			if len(chunk) > 0 {
				onToken(string(chunk))
			}
			return nil
		}),
	)
	return err
}

// streamAnthropicDirect calls the Anthropic Messages API directly via HTTP streaming,
// bypassing langchaingo to avoid its SSE parsing bugs on error events.
func streamAnthropicDirect(ctx context.Context, apiKey, baseURL, modelName, prompt string, onToken func(string)) error {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	} else {
		// ensure /v1 suffix
		baseURL = strings.TrimRight(baseURL, "/")
		if !strings.HasSuffix(baseURL, "/v1") {
			baseURL += "/v1"
		}
	}

	body, _ := json.Marshal(map[string]interface{}{
		"model":      modelName,
		"max_tokens": 4096,
		"stream":     true,
		"messages": []map[string]interface{}{
			{"role": "user", "content": prompt},
		},
	})

	httpReq, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodPost, baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build anthropic request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := nethttp.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, errBody)
	}

	scanner := bufio.NewScanner(resp.Body)
	var eventCount, textCount, thinkingCount, otherCount, skipCount int
	for scanner.Scan() {
		line := scanner.Text()
		// Anthropic SSE lines may be "data: {...}" or "data:{...}" (no space)
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
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"delta"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			skipCount++
			continue // skip malformed / non-JSON events
		}
		eventCount++
		switch event.Type {
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				if event.Delta.Text != "" {
					textCount++
					onToken(event.Delta.Text)
				}
			case "thinking_delta":
				thinkingCount++
			default:
				otherCount++
			}
		case "error":
			return fmt.Errorf("anthropic stream error: %s – %s", event.Error.Type, event.Error.Message)
		}
	}
	log.Info().
		Str("model", modelName).
		Int("events", eventCount).
		Int("text_deltas", textCount).
		Int("thinking_deltas", thinkingCount).
		Int("other_deltas", otherCount).
		Int("skipped_lines", skipCount).
		Msg("K8s AI analysis anthropic stream finished")
	return scanner.Err()
}

// buildK8sAnalysisPrompt constructs the analysis prompt based on kind and language.
func buildK8sAnalysisPrompt(req AnalyzeK8sRequest, language string) string {
	var subject, instruct string
	switch req.AnalysisKind {
	case K8sAnalysisLogs:
		subject = "container logs"
		instruct = "Identify errors, warnings, exceptions, and abnormal patterns. Summarise the root cause and suggest remediation steps."
	case K8sAnalysisEvents:
		subject = "Kubernetes events"
		instruct = "Identify Warning-type events, scheduling failures, image pull errors, OOMKills, and other anomalies. Summarise the root cause and suggest remediation steps."
	}

	switch language {
	case "en":
		return fmt.Sprintf(`You are a senior SRE. Analyse the following %s for %s "%s/%s".

%s

## Raw Data

%s`,
			subject, req.ResourceKind, req.Namespace, req.Name, instruct, req.Content)
	default: // zh / auto → Chinese
		var subjectZh string
		if req.AnalysisKind == K8sAnalysisLogs {
			subjectZh = "容器日志"
		} else {
			subjectZh = "Kubernetes 事件"
		}
		return fmt.Sprintf(`你是一位资深 SRE。请分析以下 %s %s "%s/%s" 的%s。

请识别错误、异常、警告、掉 Pod 原因等问题，给出简明准确的根因分析和修复建议。使用 Markdown 格式输出。

## 原始数据

%s`, req.ResourceKind, subjectZh, req.Namespace, req.Name, subjectZh, req.Content)
	}
}

// anthropicAnalysisSystemPrompt returns a concise system prompt for the direct
// Anthropic ReAct loop used by Analyze.
func anthropicAnalysisSystemPrompt(language string) string {
	switch language {
	case "en":
		return "You are a senior SRE performing root cause analysis on a production incident. Follow the ReAct protocol exactly: think with `Thought:`, then either call a tool with `Action:` and `Action Input:`, or finish with `Final Answer:`. Keep the protocol keywords in English; only the content after `Final Answer:` should be in English."
	case "auto":
		return "You are a senior SRE performing root cause analysis on a production incident. Follow the ReAct protocol exactly: think with `Thought:`, then either call a tool with `Action:` and `Action Input:`, or finish with `Final Answer:`. Keep the protocol keywords in English; the content after `Final Answer:` should match the language of the incident."
	default:
		return "你是一名资深 SRE，正在为线上故障执行根因分析。请严格遵循 ReAct 协议：先用 `Thought:` 思考，然后要么通过 `Action:` 和 `Action Input:` 调用工具，要么用 `Final Answer:` 给出最终报告。协议关键字（Thought/Action/Action Input/Observation/Final Answer）必须保持英文，只有 `Final Answer:` 之后的内容使用简体中文。"
	}
}

// anthropicChatSystemPrompt returns a concise system prompt for the direct
// Anthropic ReAct loop used by Chat.
func anthropicChatSystemPrompt(language string) string {
	switch language {
	case "en":
		return "You are a senior SRE answering a follow-up question about a production incident. Follow the ReAct protocol exactly: think with `Thought:`, then either call a tool with `Action:` and `Action Input:`, or finish with `Final Answer:`. Keep the protocol keywords in English; only the content after `Final Answer:` should be in English."
	case "auto":
		return "You are a senior SRE answering a follow-up question about a production incident. Follow the ReAct protocol exactly: think with `Thought:`, then either call a tool with `Action:` and `Action Input:`, or finish with `Final Answer:`. Keep the protocol keywords in English; the content after `Final Answer:` should match the language of the user's question."
	default:
		return "你是一名资深 SRE，正在回答同事关于线上故障的追问。请严格遵循 ReAct 协议：先用 `Thought:` 思考，然后要么通过 `Action:` 和 `Action Input:` 调用工具，要么用 `Final Answer:` 给出最终回答。协议关键字必须保持英文，只有 `Final Answer:` 之后的内容使用简体中文。"
	}
}

// anthropicMessage is a single message in the Anthropic Messages API.
type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicRequest is the request body for the Anthropic Messages API.
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Stream    bool               `json:"stream"`
	Messages  []anthropicMessage `json:"messages"`
}

var (
	finalAnswerRe = regexp.MustCompile(`(?s)Final Answer:\s*(.*)`)
	actionNameRe  = regexp.MustCompile(`(?s)Action:\s*(\S+)`)
	actionInputRe = regexp.MustCompile(`(?s)Action Input:\s*(.*)`)
)

// extractFinalAnswer returns the text after "Final Answer:" if present.
func extractFinalAnswer(text string) string {
	matches := finalAnswerRe.FindStringSubmatch(text)
	if len(matches) < 2 {
		return ""
	}
	s := strings.TrimSpace(matches[1])
	if s == "" {
		return ""
	}
	return s
}

// extractAction parses a ReAct step and returns the tool name and input.
func extractAction(text string) (string, string) {
	nameMatch := actionNameRe.FindStringSubmatch(text)
	inputMatch := actionInputRe.FindStringSubmatch(text)
	if len(nameMatch) < 2 || len(inputMatch) < 2 {
		return "", ""
	}
	return strings.TrimSpace(nameMatch[1]), strings.TrimSpace(inputMatch[1])
}

// executeTool finds and calls the named tool.
func executeTool(ctx context.Context, toolSet []tools.Tool, name, input string) (string, error) {
	for _, t := range toolSet {
		if t.Name() == name {
			return t.Call(ctx, input)
		}
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}

// callAnthropicMessages makes a streaming request to the Anthropic Messages
// API and returns the concatenated text content (ignoring thinking blocks and
// any other non-text deltas). Streaming is used to match the behaviour of the
// existing K8s analysis path and the LLM-provider connectivity test.
func callAnthropicMessages(ctx context.Context, apiKey, baseURL, modelName, systemPrompt string, messages []anthropicMessage) (string, error) {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	} else {
		baseURL = strings.TrimRight(baseURL, "/")
		if !strings.HasSuffix(baseURL, "/v1") {
			baseURL += "/v1"
		}
	}

	body, _ := json.Marshal(anthropicRequest{
		Model:     modelName,
		MaxTokens: 4096,
		System:    systemPrompt,
		Stream:    true,
		Messages:  messages,
	})

	httpReq, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodPost, baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build anthropic request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := nethttp.DefaultClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return "", fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, errBody)
	}

	scanner := bufio.NewScanner(resp.Body)
	var text strings.Builder
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
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"delta"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		switch event.Type {
		case "content_block_delta":
			if event.Delta.Type == "text_delta" {
				text.WriteString(event.Delta.Text)
			}
		case "error":
			return "", fmt.Errorf("anthropic stream error: %s – %s", event.Error.Type, event.Error.Message)
		}
	}
	return text.String(), scanner.Err()
}

// runAnthropicReAct implements a ReAct agent directly against the Anthropic
// Messages API. It is used for both Analyze and Chat when the configured LLM
// provider is Anthropic, bypassing langchaingo to avoid parsing failures on
// Claude thinking blocks.
func runAnthropicReAct(ctx context.Context, apiKey, baseURL, modelName, systemPrompt, initialUserPrompt string, toolSet []tools.Tool, maxIterations int, cb callbacks.Handler) (string, error) {
	messages := []anthropicMessage{{Role: "user", Content: initialUserPrompt}}

	for i := 0; i < maxIterations; i++ {
		if cb != nil {
			cb.HandleLLMStart(ctx, nil)
		}

		assistantText, err := callAnthropicMessages(ctx, apiKey, baseURL, modelName, systemPrompt, messages)
		if err != nil {
			if cb != nil {
				cb.HandleLLMError(ctx, err)
			}
			return "", err
		}

		if cb != nil {
			cb.HandleText(ctx, assistantText)
		}

		messages = append(messages, anthropicMessage{Role: "assistant", Content: assistantText})

		// Prefer Final Answer: the model is done.
		if finalAnswer := extractFinalAnswer(assistantText); finalAnswer != "" {
			if cb != nil {
				cb.HandleAgentFinish(ctx, schema.AgentFinish{ReturnValues: map[string]any{"output": finalAnswer}})
			}
			return finalAnswer, nil
		}

		// Otherwise expect a tool call.
		toolName, toolInput := extractAction(assistantText)
		if toolName == "" {
			return "", fmt.Errorf("model response contained neither Final Answer nor valid Action/Action Input")
		}

		if cb != nil {
			cb.HandleAgentAction(ctx, schema.AgentAction{Tool: toolName, ToolInput: toolInput, Log: assistantText})
			cb.HandleToolStart(ctx, toolInput)
		}

		toolResult, err := executeTool(ctx, toolSet, toolName, toolInput)
		if err != nil {
			toolResult = fmt.Sprintf("tool execution error: %v", err)
		}

		if cb != nil {
			cb.HandleToolEnd(ctx, toolResult)
		}

		log.Debug().
			Str("tool", toolName).
			Int("iteration", i+1).
			Int("result_len", len(toolResult)).
			Msg("anthropic ReAct tool executed")

		messages = append(messages, anthropicMessage{Role: "user", Content: "Observation: " + toolResult})
	}

	return "", fmt.Errorf("exceeded maximum %d ReAct iterations without Final Answer", maxIterations)
}

// ─── Period Report ───────────────────────────────────────────────────────────

// PeriodReportInput carries the metadata for a periodic alert report.
type PeriodReportInput struct {
	Period    string    // "day" / "week" / "month"
	StartTime time.Time // inclusive
	EndTime   time.Time // exclusive
}

// AnalyzePeriod queries incidents in [input.StartTime, input.EndTime) and
// calls the default LLM to generate a Markdown summary report.
// Each token is forwarded to onToken as it arrives (streaming).
func (a *Agent) AnalyzePeriod(ctx context.Context, input PeriodReportInput, onToken func(string)) error {
	var incidents []model.Incident
	if err := a.db.WithContext(ctx).
		Preload("Alerts").
		Where("opened_at >= ? AND opened_at < ?", input.StartTime, input.EndTime).
		Order("opened_at ASC").
		Find(&incidents).Error; err != nil {
		return fmt.Errorf("load incidents: %w", err)
	}

	var provider model.LLMProvider
	if err := a.db.WithContext(ctx).Where("is_default = ? AND is_enabled = ?", true, true).First(&provider).Error; err != nil {
		return fmt.Errorf("no default LLM provider configured: %w", err)
	}

	apiKey, err := a.decryptAPIKey(provider.APIKey)
	if err != nil {
		return fmt.Errorf("decrypt LLM API key: %w", err)
	}

	language := resolveLanguage(provider)
	prompt := buildPeriodReportPrompt(input, incidents, language)

	log.Info().
		Str("period", input.Period).
		Time("start", input.StartTime).
		Time("end", input.EndTime).
		Int("incident_count", len(incidents)).
		Str("provider", provider.Provider).
		Msg("starting period AI report")

	if provider.Provider == "anthropic" {
		return streamAnthropicDirect(ctx, apiKey, provider.BaseURL, provider.ModelName, prompt, onToken)
	}

	llmModel, err := newLLMFromProvider(provider, apiKey, nil)
	if err != nil {
		return fmt.Errorf("create LLM client: %w", err)
	}
	_, err = llmModel.GenerateContent(ctx,
		[]llms.MessageContent{
			{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextPart(prompt)}},
		},
		llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
			if len(chunk) > 0 {
				onToken(string(chunk))
			}
			return nil
		}),
	)
	return err
}

// buildPeriodReportPrompt constructs the LLM prompt for a periodic alert report.
func buildPeriodReportPrompt(input PeriodReportInput, incidents []model.Incident, language string) string {
	type resourceKey struct{ title, source string }
	freq := map[resourceKey]int{}
	sevCount := map[string]int{}
	var totalDurMins float64
	var maxDurMins float64
	resolved := 0
	now := time.Now()

	for _, inc := range incidents {
		rk := resourceKey{title: inc.Title, source: inc.Source}
		freq[rk]++
		sevCount[inc.Severity]++
		var durMins float64
		if inc.ResolvedAt != nil {
			durMins = inc.ResolvedAt.Sub(inc.OpenedAt).Minutes()
			totalDurMins += durMins
			resolved++
		} else {
			// still open: count elapsed time
			durMins = now.Sub(inc.OpenedAt).Minutes()
		}
		if durMins > maxDurMins {
			maxDurMins = durMins
		}
	}

	type freqEntry struct {
		title  string
		source string
		count  int
	}
	entries := make([]freqEntry, 0, len(freq))
	for rk, cnt := range freq {
		entries = append(entries, freqEntry{title: rk.title, source: rk.source, count: cnt})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].count > entries[j].count })
	if len(entries) > 10 {
		entries = entries[:10]
	}

	var topN strings.Builder
	for i, e := range entries {
		fmt.Fprintf(&topN, "%d. [%s] %s (告警次数: %d)\n", i+1, e.source, e.title, e.count)
	}

	var avgDur, maxDur string
	if resolved > 0 {
		avg := totalDurMins / float64(resolved)
		if avg < 60 {
			avgDur = fmt.Sprintf("%.0f 分钟", avg)
		} else {
			avgDur = fmt.Sprintf("%.1f 小时", avg/60)
		}
	} else {
		avgDur = "N/A"
	}
	if maxDurMins > 0 {
		if maxDurMins < 60 {
			maxDur = fmt.Sprintf("%.0f 分钟", maxDurMins)
		} else {
			maxDur = fmt.Sprintf("%.1f 小时", maxDurMins/60)
		}
	} else {
		maxDur = "N/A"
	}

	periodZh := map[string]string{"day": "天", "week": "周", "month": "月"}[input.Period]
	if periodZh == "" {
		periodZh = input.Period
	}

	if language == "en" {
		return fmt.Sprintf(`You are a senior SRE preparing a periodic alert report.

## Report Period
- Period: %s (%s → %s)

## Alert Statistics
- Total incidents: %d  Resolved: %d  Avg resolution: %s  Max duration: %s
- By severity: P0=%d P1=%d P2=%d P3=%d

## Top Frequent Resources
%s
## All Incidents (chronological, duration=(進行中) means still open)
%s
Write a concise Markdown report with sections: Overview, Top Alert Resources, Severity Trend, Duration Analysis, Key Findings, Recommendations.`,
			input.Period,
			input.StartTime.Format("2006-01-02 15:04"),
			input.EndTime.Format("2006-01-02 15:04"),
			len(incidents), resolved, avgDur, maxDur,
			sevCount["P0"], sevCount["P1"], sevCount["P2"], sevCount["P3"],
			topN.String(),
			buildIncidentListBlock(incidents),
		)
	}

	// zh / auto
	return fmt.Sprintf(`你是一名资深 SRE，正在撰写一份告警周期报告。

## 报告周期
- 周期类型：按%s
- 统计范围：%s ～ %s

## 告警统计
- 总事件数：%d
- 已解决：%d
- 平均处理时长：%s
- 最长持续时长：%s（进行中的事件按已经持续时间计入）
- 按严重等级：P0=%d  P1=%d  P2=%d  P3=%d

## 高频告警资源 TOP 10
%s
## 全部事件列表（时间顺序，duration=进行中表示尚未关闭）
%s
请以 Markdown 格式输出，必须包含以下章节（使用以下中文标题）：

### 概览
### 高频告警资源分析
### 严重等级分布
### 持续时长分析
### 关键发现
### 优化建议
`,
		periodZh,
		input.StartTime.Format("2006-01-02 15:04"),
		input.EndTime.Format("2006-01-02 15:04"),
		len(incidents), resolved, avgDur, maxDur,
		sevCount["P0"], sevCount["P1"], sevCount["P2"], sevCount["P3"],
		topN.String(),
		buildIncidentListBlock(incidents),
	)
}

// fmtDurMins returns a human-readable duration string for the given minutes.
func fmtDurMins(mins float64) string {
	if mins < 60 {
		return fmt.Sprintf("%.0fm", mins)
	}
	return fmt.Sprintf("%.1fh", mins/60)
}

// buildIncidentListBlock renders a compact incident list for the prompt (max 50 rows).
// Unresolved incidents show elapsed time up to now so the LLM can reason about ongoing alerts.
func buildIncidentListBlock(incidents []model.Incident) string {
	const maxIncidents = 50
	now := time.Now()
	var b strings.Builder
	for i, inc := range incidents {
		if i >= maxIncidents {
			fmt.Fprintf(&b, "\n... (共 %d 条，已截断显示前 %d 条)\n", len(incidents), maxIncidents)
			break
		}
		var dur string
		if inc.ResolvedAt != nil {
			dur = fmtDurMins(inc.ResolvedAt.Sub(inc.OpenedAt).Minutes())
		} else {
			// still open: show elapsed time as a lower bound
			dur = fmtDurMins(now.Sub(inc.OpenedAt).Minutes()) + "(进行中)"
		}
		fmt.Fprintf(&b, "- [%s][%s] %s  source=%s  alerts=%d  status=%s  duration=%s\n",
			inc.OpenedAt.Format("01-02 15:04"),
			inc.Severity, inc.Title, inc.Source, len(inc.Alerts), inc.Status, dur)
	}
	return b.String()
}
