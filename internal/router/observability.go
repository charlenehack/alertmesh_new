package router

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	restful "github.com/emicklei/go-restful/v3"
	"gorm.io/gorm"

	"github.com/kuzane/alertmesh/internal/ai"
	"github.com/kuzane/alertmesh/internal/config"
	"github.com/kuzane/alertmesh/internal/httputil"
	"github.com/kuzane/alertmesh/internal/label"
	"github.com/kuzane/alertmesh/internal/model"
)

// observabilityHandler exposes natural-language observability query
// generation and execution for Prometheus and OpenSearch/Elasticsearch.
type observabilityHandler struct {
	db    *gorm.DB
	cfg   *config.Config
	agent *ai.Agent
	httpc *http.Client
}

func newObservabilityHandler(db *gorm.DB, cfg *config.Config) *observabilityHandler {
	return &observabilityHandler{
		db:    db,
		cfg:   cfg,
		agent: ai.NewAgent(db, cfg),
		httpc: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				TLSHandshakeTimeout: 5 * time.Second,
			},
		},
	}
}

func (h *observabilityHandler) registerRoutes(ws *restful.WebService) {
	ws.Route(ws.POST("/observability/generate").To(h.generateQuery).
		Doc("Generate a query from natural language (PromQL or OpenSearch DSL)").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable).
		Metadata(label.MetaIdentity, "ObservabilityGenerate").
		Metadata(label.MetaModule, "observability").
		Metadata(label.MetaKind, "Observability").
		Reads(generateQueryRequest{}).
		Returns(200, "OK", generateQueryResponse{}))

	ws.Route(ws.POST("/observability/execute").To(h.executeQuery).
		Doc("Execute a generated query against a data source").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable).
		Metadata(label.MetaIdentity, "ObservabilityExecute").
		Metadata(label.MetaModule, "observability").
		Metadata(label.MetaKind, "Observability").
		Reads(executeQueryRequest{}).
		Returns(200, "OK", map[string]any{}))

	ws.Route(ws.POST("/observability/summarize").To(h.summarizeResult).
		Doc("Summarize an observability query result with the default LLM").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable).
		Metadata(label.MetaIdentity, "ObservabilitySummarize").
		Metadata(label.MetaModule, "observability").
		Metadata(label.MetaKind, "Observability").
		Reads(summarizeResultRequest{}).
		Returns(200, "OK", summarizeResultResponse{}))

	ws.Route(ws.GET("/observability/queries").To(h.listSavedQueries).
		Doc("List saved observability queries").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable).
		Metadata(label.MetaIdentity, "ObservabilityQueryList").
		Metadata(label.MetaModule, "observability").
		Metadata(label.MetaKind, "Observability").
		Returns(200, "OK", []model.SavedQuery{}))

	ws.Route(ws.POST("/observability/queries").To(h.createSavedQuery).
		Doc("Save an observability query").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable).
		Metadata(label.MetaIdentity, "ObservabilityQueryCreate").
		Metadata(label.MetaModule, "observability").
		Metadata(label.MetaKind, "Observability").
		Reads(savedQueryRequest{}).
		Returns(201, "Created", model.SavedQuery{}))

	ws.Route(ws.GET("/observability/queries/{id}").To(h.getSavedQuery).
		Doc("Get a saved observability query").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable).
		Metadata(label.MetaIdentity, "ObservabilityQueryGet").
		Metadata(label.MetaModule, "observability").
		Metadata(label.MetaKind, "Observability").
		Param(ws.PathParameter("id", "query id").DataType("string")))

	ws.Route(ws.DELETE("/observability/queries/{id}").To(h.deleteSavedQuery).
		Doc("Delete a saved observability query").
		Metadata(label.MetaAuth, label.Enable).
		Metadata(label.MetaACL, label.Enable).
		Metadata(label.MetaIdentity, "ObservabilityQueryDelete").
		Metadata(label.MetaModule, "observability").
		Metadata(label.MetaKind, "Observability").
		Param(ws.PathParameter("id", "query id").DataType("string")))
}

// ─── Request/Response DTOs ───────────────────────────────────────────────────

type generateQueryRequest struct {
	DataSourceKind  string `json:"data_source_kind"`  // prometheus / opensearch / elastic
	NaturalLanguage string `json:"natural_language"` // e.g. 近10分钟nginx非200状态码
}

type generateQueryResponse struct {
	QueryText string `json:"query_text"`
}

type executeQueryRequest struct {
	DataSourceKind string `json:"data_source_kind"`            // prometheus / opensearch / elastic
	DataSourceID   string `json:"data_source_id,omitempty"`    // optional for prometheus fallback
	QueryText      string `json:"query_text"`                  // PromQL or ES DSL JSON
	StartTime      string `json:"start_time,omitempty"`        // RFC3339 or unix
	EndTime        string `json:"end_time,omitempty"`          // RFC3339 or unix
	Step           string `json:"step,omitempty"`              // Prometheus step only
}

type savedQueryRequest struct {
	Name            string  `json:"name"`
	DataSourceKind  string  `json:"data_source_kind"`
	DataSourceID    *string `json:"data_source_id,omitempty"`
	NaturalLanguage string  `json:"natural_language"`
	QueryText       string  `json:"query_text"`
	IsShared        bool    `json:"is_shared"`
}

type summarizeResultRequest struct {
	DataSourceKind  string `json:"data_source_kind"`
	NaturalLanguage string `json:"natural_language"`
	Result          any    `json:"result"`
}

type summarizeResultResponse struct {
	Summary string `json:"summary"`
}

// ─── Natural language → query ────────────────────────────────────────────────

func (h *observabilityHandler) generateQuery(req *restful.Request, resp *restful.Response) {
	var in generateQueryRequest
	if err := req.ReadEntity(&in); err != nil {
		httputil.BadRequest(resp, "invalid request body: "+err.Error())
		return
	}
	if in.DataSourceKind == "" || in.NaturalLanguage == "" {
		httputil.BadRequest(resp, "data_source_kind and natural_language are required")
		return
	}

	ctx := req.Request.Context()
	var buf strings.Builder
	err := h.agent.GenerateObservabilityQuery(ctx, in.DataSourceKind, in.NaturalLanguage, func(tok string) {
		buf.WriteString(tok)
	})
	if err != nil {
		httputil.InternalError(resp, "failed to generate query: "+err.Error())
		return
	}

	query := strings.TrimSpace(buf.String())
	// Strip common markdown fences if the model ignored the instruction.
	query = stripMarkdownFence(query)

	httputil.Success(resp, generateQueryResponse{QueryText: query})
}

func stripMarkdownFence(s string) string {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"```promql", "```json", "```"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			s = strings.TrimSuffix(s, "```")
			s = strings.TrimSpace(s)
			break
		}
	}
	return s
}

func (h *observabilityHandler) summarizeResult(req *restful.Request, resp *restful.Response) {
	var in summarizeResultRequest
	if err := req.ReadEntity(&in); err != nil {
		httputil.BadRequest(resp, "invalid request body: "+err.Error())
		return
	}
	if in.DataSourceKind == "" || in.NaturalLanguage == "" {
		httputil.BadRequest(resp, "data_source_kind and natural_language are required")
		return
	}

	resultJSON, err := json.Marshal(in.Result)
	if err != nil {
		httputil.InternalError(resp, "failed to marshal result: "+err.Error())
		return
	}

	ctx := req.Request.Context()
	summary, err := h.agent.SummarizeObservabilityResult(ctx, in.DataSourceKind, in.NaturalLanguage, string(resultJSON))
	if err != nil {
		httputil.InternalError(resp, "failed to summarize result: "+err.Error())
		return
	}

	httputil.Success(resp, summarizeResultResponse{Summary: summary})
}

// ─── Query execution ─────────────────────────────────────────────────────────

func (h *observabilityHandler) executeQuery(req *restful.Request, resp *restful.Response) {
	var in executeQueryRequest
	if err := req.ReadEntity(&in); err != nil {
		httputil.BadRequest(resp, "invalid request body: "+err.Error())
		return
	}
	if in.QueryText == "" {
		httputil.BadRequest(resp, "query_text is required")
		return
	}
	if err := validateReadOnlyQuery(in.DataSourceKind, in.QueryText); err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	switch strings.ToLower(in.DataSourceKind) {
	case model.DataSourceKindPrometheus:
		h.executePrometheus(req, resp, in)
	case model.DataSourceKindOpenSearch, model.DataSourceKindElastic:
		h.executeOpenSearch(req, resp, in)
	default:
		httputil.BadRequest(resp, "unsupported data_source_kind: "+in.DataSourceKind)
	}
}

func validateReadOnlyQuery(kind, query string) error {
	lower := strings.ToLower(query)
	dangerous := []string{"delete", "drop", "truncate", "update", "bulk", "_delete_by_query", "_update_by_query"}
	for _, d := range dangerous {
		if strings.Contains(lower, d) {
			return fmt.Errorf("potentially destructive keyword %q is not allowed", d)
		}
	}
	return nil
}

func (h *observabilityHandler) executePrometheus(req *restful.Request, resp *restful.Response, in executeQueryRequest) {
	endpoint := h.cfg.PrometheusURL
	if in.DataSourceID != "" {
		var ds model.DataSource
		if err := h.db.WithContext(req.Request.Context()).First(&ds, "id = ?", in.DataSourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				httputil.NotFound(resp)
				return
			}
			httputil.InternalError(resp, err.Error())
			return
		}
		if ds.Kind != model.DataSourceKindPrometheus {
			httputil.BadRequest(resp, "data source is not prometheus")
			return
		}
		endpoint = ds.Endpoint
	}
	if endpoint == "" {
		httputil.BadRequest(resp, "no prometheus endpoint configured")
		return
	}

	apiPath := "query"
	if in.StartTime != "" || in.EndTime != "" {
		apiPath = "query_range"
	}
	upstream, err := url.Parse(strings.TrimRight(endpoint, "/") + "/api/v1/" + apiPath)
	if err != nil {
		httputil.InternalError(resp, "bad endpoint: "+err.Error())
		return
	}

	q := upstream.Query()
	q.Set("query", in.QueryText)
	if in.StartTime != "" {
		q.Set("start", in.StartTime)
	}
	if in.EndTime != "" {
		q.Set("end", in.EndTime)
	}
	if in.Step != "" {
		q.Set("step", in.Step)
	} else if apiPath == "query_range" {
		q.Set("step", "15s")
	}
	upstream.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(req.Request.Context(), 30*time.Second)
	defer cancel()

	hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, upstream.String(), nil)
	res, err := h.httpc.Do(hreq)
	if err != nil {
		httputil.Error(resp, http.StatusBadGateway, "upstream call failed: "+err.Error())
		return
	}
	defer func() { _ = res.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 400 {
		httputil.Error(resp, res.StatusCode, string(body))
		return
	}
	var payload any
	_ = json.Unmarshal(body, &payload)
	httputil.Success(resp, payload)
}

func (h *observabilityHandler) executeOpenSearch(req *restful.Request, resp *restful.Response, in executeQueryRequest) {
	if in.DataSourceID == "" {
		httputil.BadRequest(resp, "data_source_id is required for opensearch/elastic")
		return
	}

	var ds model.DataSource
	if err := h.db.WithContext(req.Request.Context()).First(&ds, "id = ?", in.DataSourceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.NotFound(resp)
			return
		}
		httputil.InternalError(resp, err.Error())
		return
	}
	if ds.Kind != model.DataSourceKindOpenSearch && ds.Kind != model.DataSourceKindElastic {
		httputil.BadRequest(resp, "data source is not opensearch/elastic")
		return
	}
	if ds.Endpoint == "" {
		httputil.BadRequest(resp, "data source endpoint is empty")
		return
	}

	cfg := jsonToMap(ds.Config)
	index := asString(cfg["index"])
	if index == "" {
		httputil.BadRequest(resp, "data source has no configured index")
		return
	}

	upstream, err := url.Parse(strings.TrimRight(ds.Endpoint, "/") + "/" + index + "/_search")
	if err != nil {
		httputil.InternalError(resp, "bad endpoint: "+err.Error())
		return
	}

	body := in.QueryText
	if !strings.HasPrefix(strings.TrimSpace(body), "{") {
		// Wrap a simple query string into a match/query_string DSL.
		wrapped := map[string]any{
			"query": map[string]any{
				"query_string": map[string]any{"query": body},
			},
		}
		b, _ := json.Marshal(wrapped)
		body = string(b)
	}

	ctx, cancel := context.WithTimeout(req.Request.Context(), 30*time.Second)
	defer cancel()

	hreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, upstream.String(), bytes.NewReader([]byte(body)))
	hreq.Header.Set("Content-Type", "application/json")

	secrets, _ := h.decryptSecrets(ds.SecretEnc)
	applyHTTPAuth(hreq, cfg, secrets)

	client := h.httpc
	if asBool(cfg["tls_insecure_skip_verify"]) {
		client = &http.Client{
			Timeout: h.httpc.Timeout,
			Transport: &http.Transport{
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
				DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				TLSHandshakeTimeout: 5 * time.Second,
			},
		}
	}

	res, err := client.Do(hreq)
	if err != nil {
		httputil.Error(resp, http.StatusBadGateway, "upstream call failed: "+err.Error())
		return
	}
	defer func() { _ = res.Body.Close() }()

	rawBody, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 400 {
		httputil.Error(resp, res.StatusCode, string(rawBody))
		return
	}
	var payload any
	_ = json.Unmarshal(rawBody, &payload)
	httputil.Success(resp, payload)
}

// ─── Saved queries CRUD ──────────────────────────────────────────────────────

func (h *observabilityHandler) listSavedQueries(req *restful.Request, resp *restful.Response) {
	userID := currentUserID(req)
	var rows []model.SavedQuery
	db := h.db.WithContext(req.Request.Context()).Order("updated_at DESC")
	if userID == "" {
		db = db.Where("is_shared = ?", true)
	} else {
		db = db.Where("is_shared = ? OR created_by = ?", true, userID)
	}
	if kind := req.QueryParameter("kind"); kind != "" {
		db = db.Where("data_source_kind = ?", kind)
	}
	if err := db.Find(&rows).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, rows)
}

func (h *observabilityHandler) createSavedQuery(req *restful.Request, resp *restful.Response) {
	var in savedQueryRequest
	if err := req.ReadEntity(&in); err != nil {
		httputil.BadRequest(resp, "invalid request body: "+err.Error())
		return
	}
	if in.Name == "" || in.DataSourceKind == "" || in.QueryText == "" {
		httputil.BadRequest(resp, "name, data_source_kind and query_text are required")
		return
	}

	userID := currentUserID(req)
	var createdBy *string
	if userID != "" {
		createdBy = ptr(userID)
	}
	var dsID *string
	if in.DataSourceID != nil && *in.DataSourceID != "" {
		dsID = in.DataSourceID
	}
	row := model.SavedQuery{
		Name:            in.Name,
		DataSourceKind:  in.DataSourceKind,
		DataSourceID:    dsID,
		NaturalLanguage: in.NaturalLanguage,
		QueryText:       in.QueryText,
		IsShared:        in.IsShared,
		CreatedBy:       createdBy,
	}
	if err := h.db.WithContext(req.Request.Context()).Create(&row).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Created(resp, row)
}

func (h *observabilityHandler) getSavedQuery(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")
	userID := currentUserID(req)
	var row model.SavedQuery
	db := h.db.WithContext(req.Request.Context()).Where("id = ?", id)
	if userID == "" {
		db = db.Where("is_shared = ?", true)
	} else {
		db = db.Where("is_shared = ? OR created_by = ?", true, userID)
	}
	err := db.First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httputil.NotFound(resp)
			return
		}
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, row)
}

func (h *observabilityHandler) deleteSavedQuery(req *restful.Request, resp *restful.Response) {
	id := req.PathParameter("id")
	userID := currentUserID(req)
	if userID == "" {
		httputil.Error(resp, http.StatusUnauthorized, "authentication required")
		return
	}
	result := h.db.WithContext(req.Request.Context()).
		Where("id = ? AND created_by = ?", id, userID).
		Delete(&model.SavedQuery{})
	if result.Error != nil {
		httputil.InternalError(resp, result.Error.Error())
		return
	}
	if result.RowsAffected == 0 {
		httputil.NotFound(resp)
		return
	}
	resp.WriteHeader(http.StatusNoContent)
}

// decryptSecrets mirrors the logic in data_sources.go but avoids making that
// method public just for this MVP handler.
func (h *observabilityHandler) decryptSecrets(stored string) (map[string]string, error) {
	if stored == "" {
		return map[string]string{}, nil
	}
	if h.cfg != nil && h.cfg.EncryptionKey != "" {
		if plain, err := config.Decrypt(stored, h.cfg.EncryptionKey); err == nil {
			return parseObsSecretJSON(plain)
		}
	}
	return parseObsSecretJSON(stored)
}

func parseObsSecretJSON(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]string{}, fmt.Errorf("malformed secret blob: %w", err)
	}
	return out, nil
}

func currentUserID(req *restful.Request) string {
	id, _ := req.Attribute("user_id").(string)
	return id
}

func ptr(s string) *string { return &s }
