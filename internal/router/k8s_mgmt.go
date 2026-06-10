package router

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	restful "github.com/emicklei/go-restful/v3"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	ai_pkg "github.com/kuzane/alertmesh/internal/ai"
	cfgpkg "github.com/kuzane/alertmesh/internal/config"
	"github.com/kuzane/alertmesh/internal/httputil"
	"github.com/kuzane/alertmesh/internal/k8scache"
	"github.com/kuzane/alertmesh/internal/label"
	"github.com/kuzane/alertmesh/internal/model"
)

// k8sMgmtHandler proxies read-only Kubernetes API requests through a
// configured k8s data-source row.  The frontend passes ?ds=<data-source-id>
// on every request so the user can switch between clusters.
type k8sMgmtHandler struct {
	db    *gorm.DB
	cfg   *cfgpkg.Config
	agent *ai_pkg.Agent

	apiGroupCache sync.Map
	cacheMgr      *k8scache.Manager
}

func newK8sMgmtHandler(db *gorm.DB, cfg *cfgpkg.Config) *k8sMgmtHandler {
	return &k8sMgmtHandler{
		db:       db,
		cfg:      cfg,
		agent:    ai_pkg.NewAgent(db, cfg),
		cacheMgr: k8scache.NewManager(db, cfg),
	}
}

func (h *k8sMgmtHandler) registerRoutes(ws *restful.WebService) {
	meta := func(r *restful.RouteBuilder) *restful.RouteBuilder {
		return r.
			Metadata(label.MetaIdentity, label.K8sAccess).
			Metadata(label.MetaModule, label.K8sModuleName).
			Metadata(label.MetaKind, "K8s").
			Metadata(label.MetaAuth, label.Enable).
			Metadata(label.MetaACL, label.Enable)
	}

	// 集群列表（k8s 类型数据源）
	ws.Route(meta(ws.GET("/k8s/clusters").
		To(h.listClusters).
		Doc("List configured k8s clusters (data sources of kind=k8s)")))

	// 单集群摘要（节点状态 + metrics）
	ws.Route(meta(ws.GET("/k8s/cluster-summary").
		To(h.clusterSummary).
		Doc("Cluster summary: node status, CPU/memory usage/request rates")))

	// 单集群详情（基本信息 + 节点 + Pod + CPU/Memory 详细）
	ws.Route(meta(ws.GET("/k8s/cluster-detail").
		To(h.clusterDetail).
		Doc("Cluster detail: basic info, node status, pod counts, CPU/memory capacity/usage/request/limit")))

	// 集群概览
	ws.Route(meta(ws.GET("/k8s/overview").
		To(h.overview).
		Doc("K8s cluster overview: nodes, namespaces, workload summary")))

	// Namespaces
	ws.Route(meta(ws.GET("/k8s/namespaces").
		To(h.listNamespaces).
		Doc("List all namespaces")))

	// Pods
	ws.Route(meta(ws.GET("/k8s/pods").
		To(h.listPods).
		Doc("List pods, optional ?namespace= filter")))

	// 服务路由（Services + Ingresses）
	ws.Route(meta(ws.GET("/k8s/services").
		To(h.listServices).
		Doc("List services")))

	ws.Route(meta(ws.GET("/k8s/ingresses").
		To(h.listIngresses).
		Doc("List ingresses")))

	// Volumes（PVCs）
	ws.Route(meta(ws.GET("/k8s/pvcs").
		To(h.listPVCs).
		Doc("List PersistentVolumeClaims")))

	// 节点管理
	ws.Route(meta(ws.GET("/k8s/nodes").
		To(h.listNodes).
		Doc("List nodes with status and resource info")))
	ws.Route(meta(ws.GET("/k8s/node").
		To(h.getNode).
		Doc("Get a single node")))
	ws.Route(meta(ws.PUT("/k8s/node").
		To(h.updateNode).
		Doc("Replace a node (full PUT – used for label/taint edits)")))
	ws.Route(meta(ws.POST("/k8s/node/cordon").
		To(h.cordonNode).
		Doc("Cordon a node – mark it unschedulable")))
	ws.Route(meta(ws.POST("/k8s/node/uncordon").
		To(h.uncordonNode).
		Doc("Uncordon a node – mark it schedulable")))
	ws.Route(meta(ws.DELETE("/k8s/node").
		To(h.deleteNode).
		Doc("Delete a node from the cluster")))

	// ── Deployments ─────────────────────────────────────────────────────────
	ws.Route(meta(ws.GET("/k8s/deployments").
		To(h.listDeployments).
		Doc("List deployments")))
	ws.Route(meta(ws.GET("/k8s/deployment").
		To(h.getDeployment).
		Doc("Get a single deployment (?ds=&namespace=&name=)")))
	ws.Route(meta(ws.PUT("/k8s/deployment").
		To(h.updateDeployment).
		Doc("Replace a deployment (full PUT)")))
	ws.Route(meta(ws.POST("/k8s/deployment/scale").
		To(h.scaleDeployment).
		Doc("Scale deployment replicas (?ds=&namespace=&name=, body:{replicas:N})")))
	ws.Route(meta(ws.POST("/k8s/deployment/restart").
		To(h.restartDeployment).
		Doc("Rollout restart a deployment")))
	ws.Route(meta(ws.GET("/k8s/deployment/history").
		To(h.listDeploymentHistory).
		Doc("List revision history (ReplicaSets) for a deployment")))
	ws.Route(meta(ws.POST("/k8s/deployment/rollback").
		To(h.rollbackDeployment).
		Doc("Rollback deployment to a specific revision")))
	ws.Route(meta(ws.DELETE("/k8s/deployment").
		To(h.deleteDeployment).
		Doc("Delete a deployment")))

	// ── DaemonSets ──────────────────────────────────────────────────────────
	ws.Route(meta(ws.GET("/k8s/daemonsets").
		To(h.listDaemonSets).
		Doc("List daemonsets")))
	ws.Route(meta(ws.GET("/k8s/daemonset").
		To(h.getDaemonSet).
		Doc("Get a single daemonset")))
	ws.Route(meta(ws.PUT("/k8s/daemonset").
		To(h.updateDaemonSet).
		Doc("Replace a daemonset")))
	ws.Route(meta(ws.POST("/k8s/daemonset/restart").
		To(h.restartDaemonSet).
		Doc("Rollout restart a daemonset")))
	ws.Route(meta(ws.GET("/k8s/daemonset/history").
		To(h.listDaemonSetHistory).
		Doc("List revision history (ControllerRevisions) for a daemonset")))
	ws.Route(meta(ws.POST("/k8s/daemonset/rollback").
		To(h.rollbackDaemonSet).
		Doc("Rollback daemonset to a specific revision")))
	ws.Route(meta(ws.DELETE("/k8s/daemonset").
		To(h.deleteDaemonSet).
		Doc("Delete a daemonset")))

	// ── Pod write ────────────────────────────────────────────────────────────
	ws.Route(meta(ws.GET("/k8s/pod").
		To(h.getPod).
		Doc("Get a single pod (?ds=&namespace=&name=)")))
	ws.Route(meta(ws.DELETE("/k8s/pod").
		To(h.deletePod).
		Doc("Delete a pod")))
	ws.Route(meta(ws.GET("/k8s/pod/logs").
		To(h.podLogs).
		Doc("Get pod logs (?ds=&namespace=&name=&container=&tail=&previous=true)")))
	ws.Route(meta(ws.GET("/k8s/pod/describe").
		To(h.podDescribe).
		Doc("Get pod describe-style detail (?ds=&namespace=&name=)")))
	ws.Route(meta(ws.GET("/k8s/pod/events").
		To(h.podEvents).
		Doc("Get pod events (?ds=&namespace=&name=")))
	ws.Route(meta(ws.GET("/k8s/events").
		To(h.resourceEvents).
		Doc("Get resource events (?ds=&namespace=&name=&kind=Deployment|DaemonSet|...)")))
	ws.Route(meta(ws.POST("/k8s/pod/exec").
		To(h.podExec).
		Doc("Exec command in pod container (?ds=&namespace=&name=&container=, body:{command:string})")))
	ws.Route(meta(ws.GET("/k8s/pod/terminal").
		To(h.podTerminal).
		Consumes("*/*").
		Produces("*/*").
		Doc("WebSocket interactive terminal (?ds=&namespace=&name=&container=)")))
	ws.Route(meta(ws.POST("/k8s/pod/upload").
		To(h.podFileUpload).
		Consumes("multipart/form-data", "*/*").
		Produces(restful.MIME_JSON).
		Doc("Upload file into pod container (?ds=&namespace=&name=&container=&path=")))
	ws.Route(meta(ws.GET("/k8s/pod/download").
		To(h.podFileDownload).
		Consumes("*/*").
		Produces("application/octet-stream", "*/*").
		Doc("Download file from pod container (?ds=&namespace=&name=&container=&path=")))
	// ── Service/Ingress write ────────────────────────────────────────────────
	ws.Route(meta(ws.GET("/k8s/service").
		To(h.getService).
		Doc("Get a single service")))
	ws.Route(meta(ws.PUT("/k8s/service").
		To(h.updateService).
		Doc("Replace a service")))
	ws.Route(meta(ws.DELETE("/k8s/service").
		To(h.deleteService).
		Doc("Delete a service")))
	ws.Route(meta(ws.GET("/k8s/ingress").
		To(h.getIngress).
		Doc("Get a single ingress")))
	ws.Route(meta(ws.PUT("/k8s/ingress").
		To(h.updateIngress).
		Doc("Replace an ingress")))
	ws.Route(meta(ws.DELETE("/k8s/ingress").
		To(h.deleteIngress).
		Doc("Delete an ingress")))

	// ── PVC write ────────────────────────────────────────────────────────────
	ws.Route(meta(ws.GET("/k8s/pvc").
		To(h.getPVC).
		Doc("Get a single PVC")))
	ws.Route(meta(ws.POST("/k8s/pvc/resize").
		To(h.resizePVC).
		Doc("Resize PVC storage request")))

	// ── ConfigMaps ──────────────────────────────────────────────────────────
	ws.Route(meta(ws.GET("/k8s/configmaps").
		To(h.listConfigMaps).
		Doc("List configmaps")))
	ws.Route(meta(ws.GET("/k8s/configmap").
		To(h.getConfigMap).
		Doc("Get a single configmap")))
	ws.Route(meta(ws.PUT("/k8s/configmap").
		To(h.updateConfigMap).
		Doc("Replace a configmap")))
	ws.Route(meta(ws.DELETE("/k8s/configmap").
		To(h.deleteConfigMap).
		Doc("Delete a configmap")))

	// ── HPA ────────────────────────────────────────────────────────────────
	ws.Route(meta(ws.GET("/k8s/hpas").
		To(h.listHPAs).
		Doc("List horizontal pod autoscalers")))
	ws.Route(meta(ws.GET("/k8s/hpa").
		To(h.getHPA).
		Doc("Get a single HPA")))
	ws.Route(meta(ws.PUT("/k8s/hpa").
		To(h.updateHPA).
		Doc("Replace an HPA")))
	ws.Route(meta(ws.DELETE("/k8s/hpa").
		To(h.deleteHPA).
		Doc("Delete an HPA")))

	// ── Endpoints ──────────────────────────────────────────────────────────────
	ws.Route(meta(ws.GET("/k8s/endpoints").
		To(h.listEndpoints).
		Doc("List endpoints")))
	ws.Route(meta(ws.GET("/k8s/endpoint").
		To(h.getEndpoint).
		Doc("Get a single endpoint")))
	ws.Route(meta(ws.PUT("/k8s/endpoint").
		To(h.updateEndpoint).
		Doc("Replace an endpoint")))
	ws.Route(meta(ws.DELETE("/k8s/endpoint").
		To(h.deleteEndpoint).
		Doc("Delete an endpoint")))

	// ── AI analysis (SSE streaming) ─────────────────────────────────────────
	ws.Route(meta(ws.POST("/k8s/ai/analyze").
		To(h.k8sAIAnalyze).
		Doc("AI analysis of pod logs or K8s events (SSE streaming)")))

	ws.Route(meta(ws.GET("/k8s/pod/metrics").
		To(h.podMetrics).
		Doc("Get pod CPU/memory usage from metrics-server (?ds=&namespace=)")))

	ws.Route(meta(ws.GET("/k8s/global/events").
		To(h.globalEvents).
		Doc("Get all events across namespaces (?ds=&namespace=&type=Warning|Normal)")))
}

// ─── cluster list ──────────────────────────────────────────────────────────────

func (h *k8sMgmtHandler) listClusters(req *restful.Request, resp *restful.Response) {
	var rows []model.DataSource
	if err := h.db.WithContext(req.Request.Context()).
		Where("kind = ?", model.DataSourceKindK8s).
		Select("id, name, description, endpoint, is_enabled, is_default, config, last_test_ok, last_test_at").
		Order("name").
		Find(&rows).Error; err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, rows)
}

// ─── proxy helpers ─────────────────────────────────────────────────────────────

// k8sClient returns an HTTP client + base URL for the given data source.
func (h *k8sMgmtHandler) k8sClient(dsID string) (client *http.Client, baseURL string, token string, err error) {
	var row model.DataSource
	if err = h.db.Where("id = ? AND kind = ?", dsID, model.DataSourceKindK8s).First(&row).Error; err != nil {
		return nil, "", "", fmt.Errorf("cluster not found: %s", dsID)
	}
	cfg := jsonToMap(row.Config)

	secrets := map[string]string{}
	if row.SecretEnc != "" && h.cfg != nil && h.cfg.EncryptionKey != "" {
		plain, decErr := cfgpkg.Decrypt(row.SecretEnc, h.cfg.EncryptionKey)
		if decErr != nil {
			return nil, "", "", fmt.Errorf("数据源 %s 凭证解密失败: %w", dsID, decErr)
		}
		if err := json.Unmarshal([]byte(plain), &secrets); err != nil {
			return nil, "", "", fmt.Errorf("数据源 %s 凭证 JSON 解析失败: %w", dsID, err)
		}
	}

	if asBool(cfg["in_cluster"]) {
		// in-cluster: use the ServiceAccount token mounted by Kubernetes
		baseURL = "https://kubernetes.default.svc"
		token = "" // will use in-cluster SA token if available; for now skip auth
	} else {
		baseURL = strings.TrimRight(row.Endpoint, "/")
		token = strings.TrimSpace(secrets["token"])
	}

	tlsSkip := asBool(cfg["tls_insecure_skip_verify"])
	transport := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: tlsSkip}, //nolint:gosec
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	client = &http.Client{Timeout: 60 * time.Second, Transport: transport}
	return client, baseURL, token, nil
}

// proxyK8s makes a GET request to the Kubernetes API server and returns the
// parsed JSON body.  Any non-2xx response is surfaced as an error.
func (h *k8sMgmtHandler) proxyK8s(req *restful.Request, resp *restful.Response, path string) {
	dsID := req.QueryParameter("ds")
	if dsID == "" {
		httputil.BadRequest(resp, "缺少 ?ds=<cluster-id> 参数")
		return
	}

	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	// Build query params: forward caller params (strip ds=)
	// Frontend can pass ?limit=N and ?continue=token for pagination.
	// If no limit specified, default to 500 to avoid pulling entire cluster.
	params := req.Request.URL.Query()
	params.Del("ds")
	if params.Get("limit") == "" && params.Get("continue") == "" {
		params.Set("limit", "500")
	}
	fullURL := baseURL + path
	if rest := params.Encode(); rest != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		fullURL += sep + rest
	}

	k8sReq, _ := http.NewRequestWithContext(req.Request.Context(), http.MethodGet, fullURL, nil)
	if token != "" {
		k8sReq.Header.Set("Authorization", "Bearer "+token)
	}
	k8sReq.Header.Set("Accept", "application/json")

	k8sResp, err := client.Do(k8sReq)
	if err != nil {
		httputil.InternalError(resp, "Kubernetes API 请求失败: "+err.Error())
		return
	}
	defer func() { _ = k8sResp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(k8sResp.Body, 32*1024*1024)) // 32 MB max
	if k8sResp.StatusCode >= 400 {
		httputil.Error(resp, k8sResp.StatusCode, fmt.Sprintf("Kubernetes API error: %s", string(body)))
		return
	}

	var result any
	if err := json.Unmarshal(body, &result); err != nil {
		// body may have been truncated – return what we have as text for diagnosis
		httputil.InternalError(resp, fmt.Sprintf("解析 Kubernetes API 响应失败 (body=%d bytes): %v", len(body), err))
		return
	}
	httputil.Success(resp, result)
}

// proxyK8sText proxies a GET request to the K8s API and returns the raw text body.
func (h *k8sMgmtHandler) proxyK8sText(req *restful.Request, resp *restful.Response, path string) {
	dsID := req.QueryParameter("ds")
	if dsID == "" {
		httputil.BadRequest(resp, "缺少 ?ds=<cluster-id> 参数")
		return
	}

	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	fullURL := baseURL + path
	if qs := req.Request.URL.RawQuery; qs != "" {
		params := req.Request.URL.Query()
		params.Del("ds")
		if rest := params.Encode(); rest != "" {
			sep := "?"
			if strings.Contains(path, "?") {
				sep = "&"
			}
			fullURL += sep + rest
		}
	}

	k8sReq, _ := http.NewRequestWithContext(req.Request.Context(), http.MethodGet, fullURL, nil)
	if token != "" {
		k8sReq.Header.Set("Authorization", "Bearer "+token)
	}

	k8sResp, err := client.Do(k8sReq)
	if err != nil {
		httputil.InternalError(resp, "Kubernetes API 请求失败: "+err.Error())
		return
	}
	defer func() { _ = k8sResp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(k8sResp.Body, 32*1024*1024))
	if k8sResp.StatusCode >= 400 {
		httputil.Error(resp, k8sResp.StatusCode, fmt.Sprintf("Kubernetes API error: %s", string(body)))
		return
	}

	httputil.Success(resp, string(body))
}

// ─── handlers ─────────────────────────────────────────────────────────────────

func (h *k8sMgmtHandler) overview(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	if dsID == "" {
		httputil.BadRequest(resp, "缺少 ?ds=<cluster-id> 参数")
		return
	}
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}
	ctx := req.Request.Context()

	fetch := func(path string) (map[string]any, error) {
		u := baseURL + path
		r, e := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if e != nil {
			return nil, e
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Accept", "application/json")
		res, e := client.Do(r)
		if e != nil {
			return nil, e
		}
		defer func() { _ = res.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(res.Body, 32*1024*1024))
		out := map[string]any{}
		if e := json.Unmarshal(body, &out); e != nil {
			return nil, e
		}
		return out, nil
	}

	// countViaListMeta uses K8s list metadata to get total count.
	// With limit=1, K8s returns metadata.remainingItemCount (1.15+).
	// Falls back to len(items) if remainingItemCount is absent.
	countViaListMeta := func(data map[string]any) int {
		if items, ok := data["items"].([]any); ok {
			total := len(items)
			if meta, ok := data["metadata"].(map[string]any); ok {
				if rem, ok := meta["remainingItemCount"]; ok {
					switch v := rem.(type) {
					case float64:
						total += int(v)
					case json.Number:
						if n, e := v.Int64(); e == nil {
							total += int(n)
						}
					}
				}
			}
			return total
		}
		return 0
	}

	// 并发请求所有 K8s API:
	// - nodes/namespaces: 通常数量少（<500），全量拉取以统计 Ready/CPU/Memory
	// - pods/deployments/daemonsets/statefulsets: 用 limit=1 获取总数，不拉全量数据
	// - metrics: 全量拉取以汇总使用率
	g, gctx := errgroup.WithContext(ctx)

	var nodeList, nsList, podCountData, deployCountData, dsCountData, ssCountData, metricsList map[string]any

	g.Go(func() error {
		var e error
		nodeList, e = fetch("/api/v1/nodes?limit=500")
		return e
	})
	g.Go(func() error {
		var e error
		nsList, e = fetch("/api/v1/namespaces?limit=500")
		return e
	})
	g.Go(func() error {
		var e error
		podCountData, e = fetch("/api/v1/pods?limit=1")
		return e
	})
	g.Go(func() error {
		var e error
		deployCountData, e = fetch("/apis/apps/v1/deployments?limit=1")
		return e
	})
	g.Go(func() error {
		var e error
		dsCountData, e = fetch("/apis/apps/v1/daemonsets?limit=1")
		return e
	})
	g.Go(func() error {
		var e error
		ssCountData, e = fetch("/apis/apps/v1/statefulsets?limit=1")
		return e
	})
	g.Go(func() error {
		if gctx.Err() != nil {
			return gctx.Err()
		}
		var e error
		metricsList, e = fetch("/apis/metrics.k8s.io/v1beta1/nodes")
		_ = e
		return nil // metrics-server 不可用不算致命错误
	})

	if err := g.Wait(); err != nil {
		httputil.InternalError(resp, "Kubernetes API 请求失败: "+err.Error())
		return
	}

	// --- nodes ---
	totalNodes, readyNodes := 0, 0
	var totalCapCPUm, totalCapMemKi, totalAllocCPUm, totalAllocMemKi int64
	if items, ok := nodeList["items"].([]any); ok {
		for _, item := range items {
			n, _ := item.(map[string]any)
			totalNodes++
			if status, _ := n["status"].(map[string]any); status != nil {
				if conds, _ := status["conditions"].([]any); conds != nil {
					for _, c := range conds {
						cond, _ := c.(map[string]any)
						if cond["type"] == "Ready" && cond["status"] == "True" {
							readyNodes++
						}
					}
				}
				if alloc, _ := status["allocatable"].(map[string]any); alloc != nil {
					totalAllocCPUm += parseCPUm(fmt.Sprintf("%v", alloc["cpu"]))
					totalAllocMemKi += parseMemKi(fmt.Sprintf("%v", alloc["memory"]))
				}
				if cap, _ := status["capacity"].(map[string]any); cap != nil {
					totalCapCPUm += parseCPUm(fmt.Sprintf("%v", cap["cpu"]))
					totalCapMemKi += parseMemKi(fmt.Sprintf("%v", cap["memory"]))
				}
			}
		}
	}

	// --- namespaces ---
	nsCount := 0
	if items, ok := nsList["items"].([]any); ok {
		nsCount = len(items)
	}

	// --- pods: use count from metadata (no full data pull) ---
	podTotal := countViaListMeta(podCountData)
	// Per-phase breakdown not available from limit=1; set all to 0.
	// Frontend can fetch detailed pod list on demand.
	podRunning, podPending, podFailed, podSucceeded, podUnknown := 0, 0, 0, 0, 0

	// --- workloads: use count from metadata ---
	deployCount := countViaListMeta(deployCountData)
	dsCount := countViaListMeta(dsCountData)
	ssCount := countViaListMeta(ssCountData)

	// --- metrics (usage from metrics-server) ---
	var totalUsageCPUm, totalUsageMemKi int64
	metricsAvailable := false
	if metricsList != nil {
		if items, ok := metricsList["items"].([]any); ok && len(items) > 0 {
			metricsAvailable = true
			for _, item := range items {
				m, _ := item.(map[string]any)
				if usage, _ := m["usage"].(map[string]any); usage != nil {
					totalUsageCPUm += parseCPUm(fmt.Sprintf("%v", usage["cpu"]))
					totalUsageMemKi += parseMemKi(fmt.Sprintf("%v", usage["memory"]))
				}
			}
		}
	}

	cpuUsageRate := rateOrNeg(totalUsageCPUm, totalCapCPUm)
	memUsageRate := rateOrNeg(totalUsageMemKi, totalCapMemKi)
	cpuRequestRate := rateOrNeg(totalCapCPUm-totalAllocCPUm, totalCapCPUm)
	memRequestRate := rateOrNeg(totalCapMemKi-totalAllocMemKi, totalCapMemKi)

	httputil.Success(resp, map[string]any{
		"total_nodes":       totalNodes,
		"ready_nodes":       readyNodes,
		"namespace_count":   nsCount,
		"pod_total":         podTotal,
		"pod_running":       podRunning,
		"pod_pending":       podPending,
		"pod_failed":        podFailed,
		"pod_succeeded":     podSucceeded,
		"pod_unknown":       podUnknown,
		"deployment_count":  deployCount,
		"daemonset_count":   dsCount,
		"statefulset_count": ssCount,
		"cap_cpu_m":         totalCapCPUm,
		"cap_mem_ki":        totalCapMemKi,
		"alloc_cpu_m":       totalAllocCPUm,
		"alloc_mem_ki":      totalAllocMemKi,
		"usage_cpu_m":       totalUsageCPUm,
		"usage_mem_ki":      totalUsageMemKi,
		"cpu_usage_rate":    cpuUsageRate,
		"mem_usage_rate":    memUsageRate,
		"cpu_request_rate":  cpuRequestRate,
		"mem_request_rate":  memRequestRate,
		"metrics_available": metricsAvailable,
	})
}


// parseSearchParams extracts common search/pagination params from the request.
func parseSearchParams(req *restful.Request) k8scache.SearchParams {
	return k8scache.SearchParams{
		Search:    req.QueryParameter("search"),
		Namespace: req.QueryParameter("namespace"),
		Page:      intParam(req, "page", 1),
		PageSize:  intParam(req, "pageSize", 20),
		Phase:     req.QueryParameter("phase"),
		NodeName:  req.QueryParameter("nodeName"),
		Ready:     req.QueryParameter("ready"),
	}
}

func intParam(req *restful.Request, key string, def int) int {
	v := req.QueryParameter(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// cacheResultOrProxy writes cache result if available, otherwise falls back to proxy.
func (h *k8sMgmtHandler) cacheResultOrProxy(
	req *restful.Request, resp *restful.Response,
	dsID string, result k8scache.PaginateResult, err error, fallback func(),
) {
	if err != nil {
		// Cache not available, fall back to proxy
		fallback()
		return
	}
	httputil.Success(resp, result)
}

func (h *k8sMgmtHandler) listNamespaces(req *restful.Request, resp *restful.Response) {
	h.proxyK8s(req, resp, "/api/v1/namespaces")
}

func (h *k8sMgmtHandler) listPods(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	params := parseSearchParams(req)
	result, err := h.cacheMgr.SearchPods(dsID, params)
	h.cacheResultOrProxy(req, resp, dsID, result, err, func() {
		ns := req.QueryParameter("namespace")
		path := "/api/v1/pods"
		if ns != "" {
			path = "/api/v1/namespaces/" + ns + "/pods"
		}
		h.proxyK8s(req, resp, path)
	})
}

func (h *k8sMgmtHandler) listServices(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	params := parseSearchParams(req)
	result, err := h.cacheMgr.SearchGeneric(dsID, k8scache.ResServices, params)
	h.cacheResultOrProxy(req, resp, dsID, result, err, func() {
		ns := req.QueryParameter("namespace")
		path := "/api/v1/services"
		if ns != "" {
			path = "/api/v1/namespaces/" + ns + "/services"
		}
		h.proxyK8s(req, resp, path)
	})
}

func (h *k8sMgmtHandler) listIngresses(req *restful.Request, resp *restful.Response) {
	ns := req.QueryParameter("namespace")
	dsID := req.QueryParameter("ds")
	if dsID == "" {
		httputil.BadRequest(resp, "缺少 ?ds=<cluster-id> 参数")
		return
	}
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	// 按 K8s 版本依次降级尝试 Ingress API
	groups := []string{
		"networking.k8s.io/v1",
		"networking.k8s.io/v1beta1",
		"extensions/v1beta1",
	}
	var lastBody []byte
	for _, g := range groups {
		var path string
		if ns != "" {
			path = "/apis/" + g + "/namespaces/" + ns + "/ingresses"
		} else {
			path = "/apis/" + g + "/ingresses"
		}
		fullURL := baseURL + path
		k8sReq, _ := http.NewRequestWithContext(req.Request.Context(), http.MethodGet, fullURL, nil)
		if token != "" {
			k8sReq.Header.Set("Authorization", "Bearer "+token)
		}
		k8sReq.Header.Set("Accept", "application/json")
		k8sResp, err := client.Do(k8sReq)
		if err != nil {
			httputil.InternalError(resp, "Kubernetes API 请求失败: "+err.Error())
			return
		}
		body, _ := io.ReadAll(io.LimitReader(k8sResp.Body, 32*1024*1024))
		_ = k8sResp.Body.Close()
		if k8sResp.StatusCode == http.StatusNotFound {
			lastBody = body
			continue // 该版本 API 不存在，尝试下一个
		}
		if k8sResp.StatusCode >= 400 {
			httputil.Error(resp, k8sResp.StatusCode, fmt.Sprintf("Kubernetes API error: %s", string(body)))
			return
		}
		var result any
		if err := json.Unmarshal(body, &result); err != nil {
			httputil.InternalError(resp, fmt.Sprintf("解析 Kubernetes API 响应失败 (body=%d bytes): %v", len(body), err))
			return
		}
		httputil.Success(resp, result)
		return
	}
	// 所有版本均 404
	httputil.Error(resp, http.StatusNotFound, fmt.Sprintf("该集群不支持 Ingress API: %s", string(lastBody)))
}

func (h *k8sMgmtHandler) listPVCs(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	params := parseSearchParams(req)
	result, err := h.cacheMgr.SearchGeneric(dsID, k8scache.ResPVCs, params)
	h.cacheResultOrProxy(req, resp, dsID, result, err, func() {
		ns := req.QueryParameter("namespace")
		path := "/api/v1/persistentvolumeclaims"
		if ns != "" {
			path = "/api/v1/namespaces/" + ns + "/persistentvolumeclaims"
		}
		h.proxyK8s(req, resp, path)
	})
}

func (h *k8sMgmtHandler) listNodes(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	params := parseSearchParams(req)
	result, err := h.cacheMgr.SearchNodes(dsID, params)
	h.cacheResultOrProxy(req, resp, dsID, result, err, func() {
		h.proxyK8s(req, resp, "/api/v1/nodes")
	})
}

// clusterSummary returns a lightweight summary for one cluster:
// ready/total nodes, CPU & memory usage rate (from metrics-server),
// CPU & memory request rate (from node allocatable vs pod requests).
func (h *k8sMgmtHandler) clusterSummary(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	if dsID == "" {
		httputil.BadRequest(resp, "缺少 ?ds=<cluster-id> 参数")
		return
	}
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}
	ctx := req.Request.Context()

	fetch := func(path string) (map[string]any, int) {
		u := baseURL + path
		r, e := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if e != nil {
			return nil, 0
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Accept", "application/json")
		res, e := client.Do(r)
		if e != nil {
			return nil, 0
		}
		defer func() { _ = res.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(res.Body, 32*1024*1024))
		if res.StatusCode >= 400 {
			return nil, res.StatusCode
		}
		out := map[string]any{}
		_ = json.Unmarshal(body, &out)
		return out, res.StatusCode
	}

	nodeList, nodesStatus := fetch("/api/v1/nodes?limit=500")
	metricsList, _ := fetch("/apis/metrics.k8s.io/v1beta1/nodes") // may be nil if metrics-server not installed

	// --- parse nodes ---
	totalNodes, readyNodes := 0, 0
	var totalAllocCPUm, totalAllocMemKi int64      // allocatable
	var totalCapCPUm, totalCapMemKi int64           // capacity

	if items, ok := nodeList["items"].([]any); ok {
		for _, item := range items {
			n, _ := item.(map[string]any)
			totalNodes++
			// ready check
			if status, _ := n["status"].(map[string]any); status != nil {
				if conds, _ := status["conditions"].([]any); conds != nil {
					for _, c := range conds {
						cond, _ := c.(map[string]any)
						if cond["type"] == "Ready" && cond["status"] == "True" {
							readyNodes++
						}
					}
				}
				// allocatable
				if alloc, _ := status["allocatable"].(map[string]any); alloc != nil {
					totalAllocCPUm += parseCPUm(fmt.Sprintf("%v", alloc["cpu"]))
					totalAllocMemKi += parseMemKi(fmt.Sprintf("%v", alloc["memory"]))
				}
				if cap, _ := status["capacity"].(map[string]any); cap != nil {
					totalCapCPUm += parseCPUm(fmt.Sprintf("%v", cap["cpu"]))
					totalCapMemKi += parseMemKi(fmt.Sprintf("%v", cap["memory"]))
				}
			}
		}
	}

	// --- parse metrics (usage from metrics-server) ---
	var totalUsageCPUm, totalUsageMemKi int64
	metricsAvailable := false
	if metricsList != nil {
		if items, ok := metricsList["items"].([]any); ok {
			metricsAvailable = true
			for _, item := range items {
				m, _ := item.(map[string]any)
				if usage, _ := m["usage"].(map[string]any); usage != nil {
					totalUsageCPUm += parseCPUm(fmt.Sprintf("%v", usage["cpu"]))
					totalUsageMemKi += parseMemKi(fmt.Sprintf("%v", usage["memory"]))
				}
			}
		}
	}

	// --- compute rates ---
	cpuUsageRate, memUsageRate := -1.0, -1.0
	if metricsAvailable && totalCapCPUm > 0 {
		cpuUsageRate = float64(totalUsageCPUm) / float64(totalCapCPUm) * 100
	}
	if metricsAvailable && totalCapMemKi > 0 {
		memUsageRate = float64(totalUsageMemKi) / float64(totalCapMemKi) * 100
	}

	httputil.Success(resp, map[string]any{
		"total_nodes":       totalNodes,
		"ready_nodes":       readyNodes,
		"alloc_cpu_m":       totalAllocCPUm,
		"alloc_mem_ki":      totalAllocMemKi,
		"cap_cpu_m":         totalCapCPUm,
		"cap_mem_ki":        totalCapMemKi,
		"usage_cpu_m":       totalUsageCPUm,
		"usage_mem_ki":      totalUsageMemKi,
		"cpu_usage_rate":    cpuUsageRate,    // -1 = metrics-server not available
		"mem_usage_rate":    memUsageRate,
		"cpu_request_rate":  rateOrNeg(totalCapCPUm-totalAllocCPUm, totalCapCPUm),
		"mem_request_rate":  rateOrNeg(totalCapMemKi-totalAllocMemKi, totalCapMemKi),
		"metrics_available": metricsAvailable,
		"nodes_status":      nodesStatus, // 节点 API 返回的 HTTP 状态码，0 表示请求失败
	})
}

func rateOrNeg(used, total int64) float64 {
	if total <= 0 {
		return -1
	}
	return float64(used) / float64(total) * 100
}

// parseCPUm converts a Kubernetes CPU quantity string to millicores.
// Supports: "4" (cores), "500m" (millicores), "1k" (rare).
func parseCPUm(s string) int64 {
	if s == "" || s == "<nil>" {
		return 0
	}
	if strings.HasSuffix(s, "m") {
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "m"), 10, 64)
		return v
	}
	// Kubernetes may return nano-cores from metrics-server: "123456789n"
	if strings.HasSuffix(s, "n") {
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "n"), 10, 64)
		return v / 1_000_000 // nano -> milli
	}
	v, _ := strconv.ParseFloat(s, 64)
	return int64(v * 1000)
}

// parseMemKi converts a Kubernetes memory quantity string to kibibytes.
func parseMemKi(s string) int64 {
	if s == "" || s == "<nil>" {
		return 0
	}
	switch {
	case strings.HasSuffix(s, "Ki"):
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "Ki"), 10, 64)
		return v
	case strings.HasSuffix(s, "Mi"):
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "Mi"), 10, 64)
		return v * 1024
	case strings.HasSuffix(s, "Gi"):
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "Gi"), 10, 64)
		return v * 1024 * 1024
	case strings.HasSuffix(s, "k"):
		v, _ := strconv.ParseInt(strings.TrimSuffix(s, "k"), 10, 64)
		return v
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v / 1024
}

// clusterDetail returns detailed info for a single cluster:
// basic info, node status, pod count, CPU/memory capacity/allocatable/usage/request/limit.
func (h *k8sMgmtHandler) clusterDetail(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	if dsID == "" {
		httputil.BadRequest(resp, "缺少 ?ds=<cluster-id> 参数")
		return
	}
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	// also load the data-source row for name/created_at
	var row model.DataSource
	if err := h.db.Where("id = ?", dsID).First(&row).Error; err != nil {
		httputil.BadRequest(resp, "cluster not found")
		return
	}

	ctx := req.Request.Context()
	fetch := func(path string) (map[string]any, error) {
		u := baseURL + path
		r, e := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if e != nil { return nil, e }
		if token != "" { r.Header.Set("Authorization", "Bearer "+token) }
		r.Header.Set("Accept", "application/json")
		res, e := client.Do(r)
		if e != nil { return nil, e }
		defer func() { _ = res.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(res.Body, 32*1024*1024))
		out := map[string]any{}
		if e := json.Unmarshal(body, &out); e != nil { return nil, e }
		return out, nil
	}

	// 并发拉取 nodes, pods, metrics
	g, _ := errgroup.WithContext(ctx)
	var nodeList, podList, metricsMap map[string]any

	g.Go(func() error { var e error; nodeList, e = fetch("/api/v1/nodes?limit=500"); return e })
	g.Go(func() error {
		var e error
		podList, e = fetch("/api/v1/pods?fieldSelector=status.phase!=Succeeded,status.phase!=Failed&limit=500")
		return e
	})
	g.Go(func() error {
		var e error
		metricsMap, e = fetch("/apis/metrics.k8s.io/v1beta1/nodes")
		_ = e
		return nil
	})
	if err := g.Wait(); err != nil {
		httputil.InternalError(resp, "Kubernetes API 请求失败: "+err.Error())
		return
	}

	// --- nodes ---
	totalNodes, readyNodes := 0, 0
	var capCPUm, capMemKi, allocCPUm, allocMemKi int64
	var usageCPUm, usageMemKi int64
	metricsAvail := false

	if items, ok := nodeList["items"].([]any); ok {
		for _, item := range items {
			n, _ := item.(map[string]any)
			totalNodes++
			if status, _ := n["status"].(map[string]any); status != nil {
				if conds, _ := status["conditions"].([]any); conds != nil {
					for _, c := range conds {
						cond, _ := c.(map[string]any)
						if cond["type"] == "Ready" && cond["status"] == "True" { readyNodes++ }
					}
				}
				if cap, _ := status["capacity"].(map[string]any); cap != nil {
					capCPUm  += parseCPUm(fmt.Sprintf("%v", cap["cpu"]))
					capMemKi += parseMemKi(fmt.Sprintf("%v", cap["memory"]))
				}
				if alloc, _ := status["allocatable"].(map[string]any); alloc != nil {
					allocCPUm  += parseCPUm(fmt.Sprintf("%v", alloc["cpu"]))
					allocMemKi += parseMemKi(fmt.Sprintf("%v", alloc["memory"]))
				}
			}
		}
	}

	if items, ok := metricsMap["items"].([]any); ok {
		metricsAvail = true
		for _, item := range items {
			m, _ := item.(map[string]any)
			if usage, _ := m["usage"].(map[string]any); usage != nil {
				usageCPUm  += parseCPUm(fmt.Sprintf("%v", usage["cpu"]))
				usageMemKi += parseMemKi(fmt.Sprintf("%v", usage["memory"]))
			}
		}
	}

	// --- pods: count + max pods + request + limit ---
	totalPods := 0
	var podCapacity int64 // sum of node allocatable pods
	var reqCPUm, reqMemKi, limCPUm, limMemKi int64

	if items, ok := nodeList["items"].([]any); ok {
		for _, item := range items {
			n, _ := item.(map[string]any)
			if status, _ := n["status"].(map[string]any); status != nil {
				if alloc, _ := status["allocatable"].(map[string]any); alloc != nil {
					v, _ := strconv.ParseInt(fmt.Sprintf("%v", alloc["pods"]), 10, 64)
					podCapacity += v
				}
			}
		}
	}
	if items, ok := podList["items"].([]any); ok {
		for _, item := range items {
			p, _ := item.(map[string]any)
			if phase, _ := func() (string, bool) {
				s, _ := p["status"].(map[string]any)
				v, ok := s["phase"].(string)
				return v, ok
			}(); phase != "Succeeded" && phase != "Failed" {
				totalPods++
			}
			spec, _ := p["spec"].(map[string]any)
			containers, _ := spec["containers"].([]any)
			for _, ct := range containers {
				cm, _ := ct.(map[string]any)
				res, _ := cm["resources"].(map[string]any)
				if req, _ := res["requests"].(map[string]any); req != nil {
					reqCPUm  += parseCPUm(fmt.Sprintf("%v", req["cpu"]))
					reqMemKi += parseMemKi(fmt.Sprintf("%v", req["memory"]))
				}
				if lim, _ := res["limits"].(map[string]any); lim != nil {
					limCPUm  += parseCPUm(fmt.Sprintf("%v", lim["cpu"]))
					limMemKi += parseMemKi(fmt.Sprintf("%v", lim["memory"]))
				}
			}
		}
	}

	httputil.Success(resp, map[string]any{
		// basic
		"name":        row.Name,
		"description": row.Description,
		"created_at":  row.CreatedAt,
		"is_enabled":  row.IsEnabled,
		// nodes
		"total_nodes":  totalNodes,
		"ready_nodes":  readyNodes,
		// pods
		"total_pods":   totalPods,
		"pod_capacity": podCapacity,
		// capacity (cores/GB)
		"cap_cpu_cores":   float64(capCPUm) / 1000,
		"cap_mem_gi":      float64(capMemKi) / 1024 / 1024,
		"alloc_cpu_cores": float64(allocCPUm) / 1000,
		"alloc_mem_gi":    float64(allocMemKi) / 1024 / 1024,
		// usage
		"metrics_available": metricsAvail,
		"usage_cpu_cores":   float64(usageCPUm) / 1000,
		"usage_mem_gi":      float64(usageMemKi) / 1024 / 1024,
		"cpu_usage_rate":    rateOrNeg(usageCPUm, capCPUm),
		"mem_usage_rate":    rateOrNeg(usageMemKi, capMemKi),
		// request / limit
		"req_cpu_cores": float64(reqCPUm) / 1000,
		"req_mem_gi":    float64(reqMemKi) / 1024 / 1024,
		"lim_cpu_cores": float64(limCPUm) / 1000,
		"lim_mem_gi":    float64(limMemKi) / 1024 / 1024,
		"cpu_req_rate":  rateOrNeg(reqCPUm, capCPUm),
		"mem_req_rate":  rateOrNeg(reqMemKi, capMemKi),
		"cpu_lim_rate":  rateOrNeg(limCPUm, capCPUm),
		"mem_lim_rate":  rateOrNeg(limMemKi, capMemKi),
	})
}

// ─── write helpers ─────────────────────────────────────────────────────────────

// k8sWriteReq builds and fires an HTTP request to the K8s API.
func (h *k8sMgmtHandler) k8sWriteReq(
	ctx context.Context,
	client *http.Client, baseURL, token, method, path, contentType string, body []byte,
) ([]byte, int, error) {
	u := baseURL + path
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = strings.NewReader(string(body))
	}
	r, e := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if e != nil {
		return nil, 0, e
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Accept", "application/json")
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	res, e := client.Do(r)
	if e != nil {
		return nil, 0, e
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024))
	return b, res.StatusCode, nil
}

// doWrite is a convenience wrapper used by all write handlers:
// it calls k8sWriteReq and writes httputil.Success / httputil.Error.
func (h *k8sMgmtHandler) doWrite(
	req *restful.Request, resp *restful.Response,
	method, path, contentType string, body []byte,
) {
	dsID := req.QueryParameter("ds")
	if dsID == "" {
		httputil.BadRequest(resp, "缺少 ?ds= 参数")
		return
	}
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}
	b, status, err := h.k8sWriteReq(req.Request.Context(), client, baseURL, token, method, path, contentType, body)
	if err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	if status >= 400 {
		httputil.Error(resp, status, string(b))
		return
	}
	var result any
	if len(b) > 0 {
		_ = json.Unmarshal(b, &result)
	}
	httputil.Success(resp, result)
}

// doGet fetches a single K8s resource and returns it.
func (h *k8sMgmtHandler) doGet(req *restful.Request, resp *restful.Response, path string) {
	h.proxyK8s(req, resp, path)
}

// ─── resource-specific handlers ────────────────────────────────────────────────

func nsName(req *restful.Request) (ns, name string, ok bool) {
	ns = req.QueryParameter("namespace")
	name = req.QueryParameter("name")
	if ns == "" || name == "" {
		return "", "", false
	}
	return ns, name, true
}

// ── Deployments ────────────────────────────────────────────────────────────────

func (h *k8sMgmtHandler) listDeployments(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	params := parseSearchParams(req)
	result, err := h.cacheMgr.SearchGeneric(dsID, k8scache.ResDeployments, params)
	h.cacheResultOrProxy(req, resp, dsID, result, err, func() {
		ns := req.QueryParameter("namespace")
		path := "/apis/apps/v1/deployments"
		if ns != "" {
			path = "/apis/apps/v1/namespaces/" + ns + "/deployments"
		}
		h.proxyK8s(req, resp, path)
	})
}

func (h *k8sMgmtHandler) getDeployment(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doGet(req, resp, "/apis/apps/v1/namespaces/"+ns+"/deployments/"+name)
}

func (h *k8sMgmtHandler) updateDeployment(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Request.Body, 4*1024*1024))
	h.doWrite(req, resp, http.MethodPut,
		"/apis/apps/v1/namespaces/"+ns+"/deployments/"+name,
		"application/json", body)
}

func (h *k8sMgmtHandler) scaleDeployment(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Request.Body, 64*1024))
	// body: {"replicas": N}  → merge-patch on scale sub-resource
	var in struct{ Replicas *int32 `json:"replicas"` }
	if err := json.Unmarshal(body, &in); err != nil || in.Replicas == nil {
		httputil.BadRequest(resp, "请传入 {\"replicas\": N}")
		return
	}
	patch, _ := json.Marshal(map[string]any{
		"spec": map[string]any{"replicas": *in.Replicas},
	})
	h.doWrite(req, resp, http.MethodPatch,
		"/apis/apps/v1/namespaces/"+ns+"/deployments/"+name,
		"application/merge-patch+json", patch)
}

func (h *k8sMgmtHandler) restartDeployment(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	patch, _ := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{
						"kubectl.kubernetes.io/restartedAt": now,
					},
				},
			},
		},
	})
	h.doWrite(req, resp, http.MethodPatch,
		"/apis/apps/v1/namespaces/"+ns+"/deployments/"+name,
		"application/merge-patch+json", patch)
}

func (h *k8sMgmtHandler) deleteDeployment(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doWrite(req, resp, http.MethodDelete,
		"/apis/apps/v1/namespaces/"+ns+"/deployments/"+name,
		"", nil)
}

// ── DaemonSets ─────────────────────────────────────────────────────────────────

func (h *k8sMgmtHandler) listDaemonSets(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	params := parseSearchParams(req)
	result, err := h.cacheMgr.SearchGeneric(dsID, k8scache.ResDaemonSets, params)
	h.cacheResultOrProxy(req, resp, dsID, result, err, func() {
		ns := req.QueryParameter("namespace")
		path := "/apis/apps/v1/daemonsets"
		if ns != "" {
			path = "/apis/apps/v1/namespaces/" + ns + "/daemonsets"
		}
		h.proxyK8s(req, resp, path)
	})
}

func (h *k8sMgmtHandler) getDaemonSet(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doGet(req, resp, "/apis/apps/v1/namespaces/"+ns+"/daemonsets/"+name)
}

func (h *k8sMgmtHandler) updateDaemonSet(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Request.Body, 4*1024*1024))
	h.doWrite(req, resp, http.MethodPut,
		"/apis/apps/v1/namespaces/"+ns+"/daemonsets/"+name,
		"application/json", body)
}

func (h *k8sMgmtHandler) restartDaemonSet(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	patch, _ := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{
						"kubectl.kubernetes.io/restartedAt": now,
					},
				},
			},
		},
	})
	h.doWrite(req, resp, http.MethodPatch,
		"/apis/apps/v1/namespaces/"+ns+"/daemonsets/"+name,
		"application/merge-patch+json", patch)
}

func (h *k8sMgmtHandler) deleteDaemonSet(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doWrite(req, resp, http.MethodDelete,
		"/apis/apps/v1/namespaces/"+ns+"/daemonsets/"+name,
		"", nil)
}

// ── Pods write ─────────────────────────────────────────────────────────────────

func (h *k8sMgmtHandler) getPod(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doGet(req, resp, "/api/v1/namespaces/"+ns+"/pods/"+name)
}

func (h *k8sMgmtHandler) deletePod(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doWrite(req, resp, http.MethodDelete,
		"/api/v1/namespaces/"+ns+"/pods/"+name,
		"", nil)
}

func (h *k8sMgmtHandler) podLogs(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	container := req.QueryParameter("container")
	previous := req.QueryParameter("previous")
	q := url.Values{}
	if container != "" {
		q.Set("container", container)
	}
	if previous == "true" {
		q.Set("previous", "true")
	}
	path := "/api/v1/namespaces/" + ns + "/pods/" + name + "/log"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	h.proxyK8sText(req, resp, path)
}

func (h *k8sMgmtHandler) podEvents(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	// Build directly to avoid double-encoding from proxyK8s
	dsID := req.QueryParameter("ds")
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}
	q := url.Values{}
	q.Set("fieldSelector", "involvedObject.name="+name+",involvedObject.namespace="+ns+",involvedObject.kind=Pod")
	fullURL := baseURL + "/api/v1/namespaces/" + ns + "/events?" + q.Encode()
	k8sReq, _ := http.NewRequestWithContext(req.Request.Context(), http.MethodGet, fullURL, nil)
	if token != "" {
		k8sReq.Header.Set("Authorization", "Bearer "+token)
	}
	k8sReq.Header.Set("Accept", "application/json")
	k8sResp, err := client.Do(k8sReq)
	if err != nil {
		httputil.InternalError(resp, "Kubernetes API 请求失败: "+err.Error())
		return
	}
	defer func() { _ = k8sResp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(k8sResp.Body, 8*1024*1024))
	if k8sResp.StatusCode >= 400 {
		httputil.Error(resp, k8sResp.StatusCode, fmt.Sprintf("Kubernetes API error: %s", string(body)))
		return
	}
	var result any
	if e := json.Unmarshal(body, &result); e != nil {
		httputil.InternalError(resp, fmt.Sprintf("解析响应失败: %v", e))
		return
	}
	httputil.Success(resp, result)
}

func (h *k8sMgmtHandler) resourceEvents(req *restful.Request, resp *restful.Response) {
	name := req.QueryParameter("name")
	ns := req.QueryParameter("namespace")
	if name == "" {
		httputil.BadRequest(resp, "缺少 name 参数")
		return
	}
	kind := req.QueryParameter("kind")
	if kind == "" {
		kind = "Deployment"
	}
	dsID := req.QueryParameter("ds")
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}
	q := url.Values{}
	var fullURL string
	if kind == "Node" {
		// 节点是集群级资源，走全局事件接口，用 involvedObject.name 过滤
		q.Set("fieldSelector", "involvedObject.name="+name+",involvedObject.kind=Node")
		fullURL = baseURL + "/api/v1/events?" + q.Encode()
	} else {
		if ns == "" {
			httputil.BadRequest(resp, "缺少 namespace 参数")
			return
		}
		q.Set("fieldSelector", "involvedObject.name="+name+",involvedObject.namespace="+ns+",involvedObject.kind="+kind)
		fullURL = baseURL + "/api/v1/namespaces/" + ns + "/events?" + q.Encode()
	}
	k8sReq, _ := http.NewRequestWithContext(req.Request.Context(), http.MethodGet, fullURL, nil)
	if token != "" {
		k8sReq.Header.Set("Authorization", "Bearer "+token)
	}
	k8sReq.Header.Set("Accept", "application/json")
	k8sResp, err := client.Do(k8sReq)
	if err != nil {
		httputil.InternalError(resp, "Kubernetes API 请求失败: "+err.Error())
		return
	}
	defer func() { _ = k8sResp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(k8sResp.Body, 8*1024*1024))
	if k8sResp.StatusCode >= 400 {
		httputil.Error(resp, k8sResp.StatusCode, fmt.Sprintf("Kubernetes API error: %s", string(body)))
		return
	}
	var result any
	if e := json.Unmarshal(body, &result); e != nil {
		httputil.InternalError(resp, fmt.Sprintf("解析响应失败: %v", e))
		return
	}
	httputil.Success(resp, result)
}

func (h *k8sMgmtHandler) podExec(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	container := req.QueryParameter("container")
	if container == "" {
		httputil.BadRequest(resp, "缺少 container 参数")
		return
	}

	var body struct{ Command string `json:"command"` }
	if err := req.ReadEntity(&body); err != nil || body.Command == "" {
		httputil.BadRequest(resp, "缺少 command 参数")
		return
	}

	dsID := req.QueryParameter("ds")
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	// K8s exec requires WebSocket or SPDY. Use WebSocket via gorilla/websocket.
	wsScheme := "wss"
	if strings.HasPrefix(baseURL, "http://") {
		wsScheme = "ws"
	}
	wsURL := strings.Replace(baseURL, "https://", wsScheme+"://", 1)
	wsURL = strings.Replace(wsURL, "http://", wsScheme+"://", 1)
	wsURL += "/api/v1/namespaces/" + ns + "/pods/" + name + "/exec"

	q := url.Values{}
	q.Set("container", container)
	q.Set("stdout", "true")
	q.Set("stderr", "true")
	for _, arg := range strings.Fields(body.Command) {
		q.Add("command", arg)
	}
	wsURL += "?" + q.Encode()

	tlsSkip := false
	if tr, ok := client.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
		tlsSkip = tr.TLSClientConfig.InsecureSkipVerify
	}

	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: tlsSkip}, //nolint:gosec
		HandshakeTimeout: 10 * time.Second,
	}

	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}

	wsConn, k8sResp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		msg := err.Error()
		if k8sResp != nil && k8sResp.Body != nil {
			b, _ := io.ReadAll(io.LimitReader(k8sResp.Body, 64*1024))
			_ = k8sResp.Body.Close()
			if len(b) > 0 {
				msg += " — " + string(b)
			}
		}
		httputil.InternalError(resp, "Kubernetes exec 连接失败: "+msg)
		return
	}
	defer wsConn.Close()

	// K8s exec WebSocket uses subprotocol "channel.k8s.io".
	// Each message: [1 byte channel][payload]
	// channel 1 = stdout, 2 = stderr
	var out strings.Builder
	wsConn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		_, msg, err := wsConn.ReadMessage()
		if err != nil {
			break
		}
		if len(msg) > 1 {
			ch := msg[0]
			data := msg[1:]
			if ch == 1 || ch == 2 { // stdout / stderr
				out.Write(data)
			}
		} else if len(msg) == 1 && msg[0] == 3 {
			// channel 3 = server error / close
			break
		}
	}

	httputil.Success(resp, out.String())
}

// stripExecFraming removes the 1-byte channel-prefix headers that Kubernetes
// attaches to SPDY / websocket exec streams when they fall back to HTTP.
func stripExecFraming(data []byte) []byte {
	var out []byte
	for i := 0; i < len(data); {
		if i+1 < len(data) && data[i] <= 0x03 {
			out = append(out, data[i+1])
			i += 2
		} else {
			out = append(out, data[i])
			i++
		}
	}
	return out
}

// ── Services write ─────────────────────────────────────────────────────────────

func (h *k8sMgmtHandler) getService(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doGet(req, resp, "/api/v1/namespaces/"+ns+"/services/"+name)
}

func (h *k8sMgmtHandler) updateService(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Request.Body, 4*1024*1024))
	h.doWrite(req, resp, http.MethodPut,
		"/api/v1/namespaces/"+ns+"/services/"+name,
		"application/json", body)
}

func (h *k8sMgmtHandler) deleteService(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doWrite(req, resp, http.MethodDelete,
		"/api/v1/namespaces/"+ns+"/services/"+name,
		"", nil)
}

// ── Ingresses write ────────────────────────────────────────────────────────────

// ingressAPIGroup 探测集群支持的 Ingress API group（带缓存）。
// 结果按 dsID 缓存到 apiGroupCache，避免每次请求探测。
func (h *k8sMgmtHandler) ingressAPIGroup(ctx context.Context, client *http.Client, baseURL, token, dsID, ns, name string) string {
	cacheKey := dsID + ":ingress"
	if cached, ok := h.apiGroupCache.Load(cacheKey); ok {
		return cached.(string)
	}

	groups := []string{"networking.k8s.io/v1", "networking.k8s.io/v1beta1", "extensions/v1beta1"}
	for _, g := range groups {
		path := "/apis/" + g + "/namespaces/" + ns + "/ingresses/" + name
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			h.apiGroupCache.Store(cacheKey, g)
			return g
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			h.apiGroupCache.Store(cacheKey, g)
			return g
		}
	}
	return "networking.k8s.io/v1"
}

func (h *k8sMgmtHandler) getIngress(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	dsID := req.QueryParameter("ds")
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}
	g := h.ingressAPIGroup(req.Request.Context(), client, baseURL, token, dsID, ns, name)
	h.doGet(req, resp, "/apis/"+g+"/namespaces/"+ns+"/ingresses/"+name)
}

func (h *k8sMgmtHandler) updateIngress(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	dsID := req.QueryParameter("ds")
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}
	g := h.ingressAPIGroup(req.Request.Context(), client, baseURL, token, dsID, ns, name)
	body, _ := io.ReadAll(io.LimitReader(req.Request.Body, 4*1024*1024))
	h.doWrite(req, resp, http.MethodPut,
		"/apis/"+g+"/namespaces/"+ns+"/ingresses/"+name,
		"application/json", body)
}

func (h *k8sMgmtHandler) deleteIngress(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	dsID := req.QueryParameter("ds")
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}
	g := h.ingressAPIGroup(req.Request.Context(), client, baseURL, token, dsID, ns, name)
	h.doWrite(req, resp, http.MethodDelete,
		"/apis/"+g+"/namespaces/"+ns+"/ingresses/"+name,
		"", nil)
}

// ── PVC write ──────────────────────────────────────────────────────────────────

func (h *k8sMgmtHandler) getPVC(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doGet(req, resp, "/api/v1/namespaces/"+ns+"/persistentvolumeclaims/"+name)
}

func (h *k8sMgmtHandler) resizePVC(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Request.Body, 64*1024))
	var in struct{ Storage string `json:"storage"` }
	if err := json.Unmarshal(body, &in); err != nil || in.Storage == "" {
		httputil.BadRequest(resp, "请传入 {\"storage\": \"20Gi\"}")
		return
	}
	patch, _ := json.Marshal(map[string]any{
		"spec": map[string]any{
			"resources": map[string]any{
				"requests": map[string]any{"storage": in.Storage},
			},
		},
	})
	h.doWrite(req, resp, http.MethodPatch,
		"/api/v1/namespaces/"+ns+"/persistentvolumeclaims/"+name,
		"application/merge-patch+json", patch)
}

// ── ConfigMaps ─────────────────────────────────────────────────────────────────

func (h *k8sMgmtHandler) listConfigMaps(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	params := parseSearchParams(req)
	result, err := h.cacheMgr.SearchGeneric(dsID, k8scache.ResConfigMaps, params)
	h.cacheResultOrProxy(req, resp, dsID, result, err, func() {
		ns := req.QueryParameter("namespace")
		path := "/api/v1/configmaps"
		if ns != "" {
			path = "/api/v1/namespaces/" + ns + "/configmaps"
		}
		h.proxyK8s(req, resp, path)
	})
}

func (h *k8sMgmtHandler) getConfigMap(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doGet(req, resp, "/api/v1/namespaces/"+ns+"/configmaps/"+name)
}

func (h *k8sMgmtHandler) updateConfigMap(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Request.Body, 4*1024*1024))
	h.doWrite(req, resp, http.MethodPut,
		"/api/v1/namespaces/"+ns+"/configmaps/"+name,
		"application/json", body)
}

func (h *k8sMgmtHandler) deleteConfigMap(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doWrite(req, resp, http.MethodDelete,
		"/api/v1/namespaces/"+ns+"/configmaps/"+name,
		"", nil)
}

// ── HPA ─────────────────────────────────────────────────────────────────────────

// hpaAPIPath tries autoscaling/v2 first, then falls back to v2beta2, then v1.
// Returns the full path for the list endpoint.
// hpaVersion detects the best available HPA API version (带缓存).
func (h *k8sMgmtHandler) hpaVersion(req *restful.Request) string {
	dsID := req.QueryParameter("ds")
	cacheKey := dsID + ":hpa"
	if cached, ok := h.apiGroupCache.Load(cacheKey); ok {
		return cached.(string)
	}

	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		return "v2"
	}

	candidates := []string{"v2", "v2beta2", "v1"}
	for _, ver := range candidates {
		p := "/apis/autoscaling/" + ver + "/horizontalpodautoscalers?limit=1"
		r, e := http.NewRequestWithContext(req.Request.Context(), http.MethodGet, baseURL+p, nil)
		if e != nil {
			continue
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		res, e2 := client.Do(r)
		if e2 != nil {
			continue
		}
		_ = res.Body.Close()
		if res.StatusCode == 200 || res.StatusCode == 403 {
			h.apiGroupCache.Store(cacheKey, ver)
			return ver
		}
	}
	return "v2"
}

func (h *k8sMgmtHandler) hpaListPath(req *restful.Request, ns string) (string, bool) {
	ver := h.hpaVersion(req)
	p := "/apis/autoscaling/" + ver + "/horizontalpodautoscalers"
	if ns != "" {
		p = "/apis/autoscaling/" + ver + "/namespaces/" + ns + "/horizontalpodautoscalers"
	}
	return p, true
}

func (h *k8sMgmtHandler) listHPAs(req *restful.Request, resp *restful.Response) {
	ns := req.QueryParameter("namespace")
	path, ok := h.hpaListPath(req, ns)
	if !ok {
		// No HPA API available – return empty list instead of error
		httputil.Success(resp, map[string]any{"items": []any{}, "metadata": map[string]any{}})
		return
	}
	h.proxyK8s(req, resp, path)
}

func (h *k8sMgmtHandler) hpaVersionPath(req *restful.Request, ns, name string) string {
	ver := h.hpaVersion(req)
	return "/apis/autoscaling/" + ver + "/namespaces/" + ns + "/horizontalpodautoscalers/" + name
}

func (h *k8sMgmtHandler) getHPA(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doGet(req, resp, h.hpaVersionPath(req, ns, name))
}

func (h *k8sMgmtHandler) updateHPA(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Request.Body, 4*1024*1024))
	h.doWrite(req, resp, http.MethodPut,
		h.hpaVersionPath(req, ns, name),
		"application/json", body)
}

func (h *k8sMgmtHandler) deleteHPA(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doWrite(req, resp, http.MethodDelete,
		h.hpaVersionPath(req, ns, name),
		"", nil)
}

// ── Node write ────────────────────────────────────────────────────────────────

func nodeName(req *restful.Request) (name string, ok bool) {
	name = req.QueryParameter("name")
	if name == "" {
		return "", false
	}
	return name, true
}

func (h *k8sMgmtHandler) getNode(req *restful.Request, resp *restful.Response) {
	name, ok := nodeName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 name 参数")
		return
	}
	h.doGet(req, resp, "/api/v1/nodes/"+name)
}

func (h *k8sMgmtHandler) updateNode(req *restful.Request, resp *restful.Response) {
	name, ok := nodeName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 name 参数")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Request.Body, 4*1024*1024))
	h.doWrite(req, resp, http.MethodPut,
		"/api/v1/nodes/"+name,
		"application/json", body)
}

func (h *k8sMgmtHandler) cordonNode(req *restful.Request, resp *restful.Response) {
	name, ok := nodeName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 name 参数")
		return
	}
	// PATCH: set unschedulable=true
	patch, _ := json.Marshal(map[string]any{
		"spec": map[string]any{"unschedulable": true},
	})
	h.doWrite(req, resp, http.MethodPatch,
		"/api/v1/nodes/"+name,
		"application/merge-patch+json", patch)
}

func (h *k8sMgmtHandler) uncordonNode(req *restful.Request, resp *restful.Response) {
	name, ok := nodeName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 name 参数")
		return
	}
	// PATCH: set unschedulable=false
	patch, _ := json.Marshal(map[string]any{
		"spec": map[string]any{"unschedulable": false},
	})
	h.doWrite(req, resp, http.MethodPatch,
		"/api/v1/nodes/"+name,
		"application/merge-patch+json", patch)
}

func (h *k8sMgmtHandler) deleteNode(req *restful.Request, resp *restful.Response) {
	name, ok := nodeName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 name 参数")
		return
	}
	h.doWrite(req, resp, http.MethodDelete,
		"/api/v1/nodes/"+name,
		"", nil)
}

// ── Endpoints ────────────────────────────────────────────────────────────────────

func (h *k8sMgmtHandler) listEndpoints(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	params := parseSearchParams(req)
	result, err := h.cacheMgr.SearchGeneric(dsID, k8scache.ResEndpoints, params)
	h.cacheResultOrProxy(req, resp, dsID, result, err, func() {
		ns := req.QueryParameter("namespace")
		path := "/api/v1/endpoints"
		if ns != "" {
			path = "/api/v1/namespaces/" + ns + "/endpoints"
		}
		h.proxyK8s(req, resp, path)
	})
}

func (h *k8sMgmtHandler) getEndpoint(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doGet(req, resp, "/api/v1/namespaces/"+ns+"/endpoints/"+name)
}

func (h *k8sMgmtHandler) updateEndpoint(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	body, _ := io.ReadAll(io.LimitReader(req.Request.Body, 4*1024*1024))
	h.doWrite(req, resp, http.MethodPut,
		"/api/v1/namespaces/"+ns+"/endpoints/"+name,
		"application/json", body)
}

func (h *k8sMgmtHandler) deleteEndpoint(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doWrite(req, resp, http.MethodDelete,
		"/api/v1/namespaces/"+ns+"/endpoints/"+name,
		"", nil)
}

// ─── Pod Terminal (WebSocket relay) ───────────────────────────────────────────────────────

// podTerminal upgrades the client connection to WebSocket and relays it
// to the K8s exec endpoint.  Messages in both directions use the
// "channel.k8s.io" binary framing (1-byte channel prefix).
func (h *k8sMgmtHandler) podTerminal(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	container := req.QueryParameter("container")
	if container == "" {
		httputil.BadRequest(resp, "缺少 container 参数")
		return
	}
	dsID := req.QueryParameter("ds")
	client, baseURL, token, err := h.k8sClient(dsID)
	_ = client
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	// 连接到 K8s exec 端点 (WebSocket)
	wsScheme := "wss"
	if strings.HasPrefix(baseURL, "http://") {
		wsScheme = "ws"
	}
	k8sWsURL := strings.Replace(baseURL, "https://", wsScheme+"://", 1)
	k8sWsURL = strings.Replace(k8sWsURL, "http://", wsScheme+"://", 1)
	q := url.Values{}
	q.Set("container", container)
	q.Set("stdin", "true")
	q.Set("stdout", "true")
	q.Set("stderr", "true")
	q.Set("tty", "true")
	q.Add("command", "/bin/sh")
	k8sWsURL += "/api/v1/namespaces/" + ns + "/pods/" + name + "/exec?" + q.Encode()

	tlsSkip := true // K8s API servers typically use self-signed certs
	if client.Transport != nil {
		if tr, ok := client.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
			tlsSkip = tr.TLSClientConfig.InsecureSkipVerify
		}
	}

	k8sDialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: tlsSkip}, //nolint:gosec
		HandshakeTimeout: 10 * time.Second,
		Subprotocols:     []string{"channel.k8s.io"},
	}
	k8sHeaders := http.Header{}
	if token != "" {
		k8sHeaders.Set("Authorization", "Bearer "+token)
	}
	k8sConn, k8sHResp, err := k8sDialer.Dial(k8sWsURL, k8sHeaders)
	if err != nil {
		msg := err.Error()
		if k8sHResp != nil && k8sHResp.Body != nil {
			b, _ := io.ReadAll(io.LimitReader(k8sHResp.Body, 64*1024))
			_ = k8sHResp.Body.Close()
			if len(b) > 0 { msg += " — " + string(b) }
		}
		httputil.InternalError(resp, "K8s exec 连接失败: "+msg)
		return
	}
	defer k8sConn.Close()

	// 将客户端 HTTP 升级为 WebSocket
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	clientConn, err := upgrader.Upgrade(resp.ResponseWriter, req.Request, nil)
	if err != nil {
		return
	}
	defer clientConn.Close()

	ctx, cancel := context.WithCancel(req.Request.Context())

	// K8s → 客户端
	go func() {
		defer cancel()
		for {
			_, msg, err := k8sConn.ReadMessage()
			if err != nil { return }
			if err := clientConn.WriteMessage(websocket.BinaryMessage, msg); err != nil { return }
		}
	}()

	// 客户端 → K8s
	go func() {
		defer cancel()
		for {
			_, msg, err := clientConn.ReadMessage()
			if err != nil { return }
			if err := k8sConn.WriteMessage(websocket.BinaryMessage, msg); err != nil { return }
		}
	}()

	<-ctx.Done()
}

// podFileUpload uploads a file from multipart form into a pod container via
// kubectl cp equivalent (tar pipe over exec).
func (h *k8sMgmtHandler) podFileUpload(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	container := req.QueryParameter("container")
	destPath := req.QueryParameter("path") // e.g. /tmp/
	if container == "" || destPath == "" {
		httputil.BadRequest(resp, "缺少 container 或 path 参数")
		return
	}

	if err := req.Request.ParseMultipartForm(32 << 20); err != nil {
		httputil.BadRequest(resp, "解析上传表单失败: "+err.Error())
		return
	}
	file, header, err := req.Request.FormFile("file")
	if err != nil {
		httputil.BadRequest(resp, "缺少 file 字段")
		return
	}
	defer file.Close()

	// Build tar archive in memory
	var tarBuf strings.Builder
	_ = tarBuf
	var buf strings.Builder
	_ = buf

	fileBytes, _ := io.ReadAll(io.LimitReader(file, 256<<20))
	var tarData bytes.Buffer
	tw := tar.NewWriter(&tarData)
	_ = tw.WriteHeader(&tar.Header{
		Name: header.Filename,
		Mode: 0644,
		Size: int64(len(fileBytes)),
	})
	_, _ = tw.Write(fileBytes)
	_ = tw.Close()

	dsID := req.QueryParameter("ds")
	_, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	wsScheme := "wss"
	if strings.HasPrefix(baseURL, "http://") { wsScheme = "ws" }
	wsURL := strings.Replace(baseURL, "https://", wsScheme+"://", 1)
	wsURL = strings.Replace(wsURL, "http://", wsScheme+"://", 1)

	eq := url.Values{}
	eq.Set("container", container)
	eq.Set("stdin", "true")
	eq.Set("stdout", "true")
	eq.Set("stderr", "true")
	eq.Add("command", "tar")
	eq.Add("command", "xf")
	eq.Add("command", "-")
	eq.Add("command", "-C")
	eq.Add("command", destPath)
	wsURL += "/api/v1/namespaces/" + ns + "/pods/" + name + "/exec?" + eq.Encode()

	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		HandshakeTimeout: 15 * time.Second,
		Subprotocols:     []string{"channel.k8s.io"},
	}
	headers := http.Header{}
	if token != "" { headers.Set("Authorization", "Bearer "+token) }
	wsConn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		httputil.InternalError(resp, "上传失败: "+err.Error())
		return
	}
	defer wsConn.Close()

	// channel 0 = stdin
	stdinMsg := append([]byte{0}, tarData.Bytes()...)
	_ = wsConn.WriteMessage(websocket.BinaryMessage, stdinMsg)
	// send close on channel 0
	_ = wsConn.WriteMessage(websocket.BinaryMessage, []byte{0})

	var errOut strings.Builder
	wsConn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, msg, err := wsConn.ReadMessage()
		if err != nil { break }
		if len(msg) > 1 && msg[0] == 2 {
			errOut.Write(msg[1:])
		}
	}
	if errOut.Len() > 0 && strings.TrimSpace(errOut.String()) != "" {
		httputil.InternalError(resp, "上传到容器失败: "+errOut.String())
		return
	}
	httputil.Success(resp, map[string]string{"message": "上传成功"})
}

// podFileDownload downloads a file from a pod container via tar pipe.
func (h *k8sMgmtHandler) podFileDownload(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	container := req.QueryParameter("container")
	filePath := req.QueryParameter("path") // e.g. /tmp/app.log
	if container == "" || filePath == "" {
		httputil.BadRequest(resp, "缺少 container 或 path 参数")
		return
	}

	dsID := req.QueryParameter("ds")
	_, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	wsScheme := "wss"
	if strings.HasPrefix(baseURL, "http://") { wsScheme = "ws" }
	wsURL := strings.Replace(baseURL, "https://", wsScheme+"://", 1)
	wsURL = strings.Replace(wsURL, "http://", wsScheme+"://", 1)

	eq := url.Values{}
	eq.Set("container", container)
	eq.Set("stdin", "false")
	eq.Set("stdout", "true")
	eq.Set("stderr", "true")
	eq.Add("command", "tar")
	eq.Add("command", "cf")
	eq.Add("command", "-")
	eq.Add("command", filePath)
	wsURL += "/api/v1/namespaces/" + ns + "/pods/" + name + "/exec?" + eq.Encode()

	dialer := websocket.Dialer{
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		HandshakeTimeout: 15 * time.Second,
		Subprotocols:     []string{"channel.k8s.io"},
	}
	headers := http.Header{}
	if token != "" { headers.Set("Authorization", "Bearer "+token) }
	wsConn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		httputil.InternalError(resp, "下载失败: "+err.Error())
		return
	}
	defer wsConn.Close()

	var tarData bytes.Buffer
	wsConn.SetReadDeadline(time.Now().Add(30 * time.Second))
	for {
		_, msg, err := wsConn.ReadMessage()
		if err != nil { break }
		if len(msg) > 1 && msg[0] == 1 { // stdout
			tarData.Write(msg[1:])
		}
	}

	// Extract from tar
	tr := tar.NewReader(&tarData)
	for {
		_, err := tr.Next()
		if err == io.EOF { break }
		if err != nil {
			httputil.InternalError(resp, "解析tar失败: "+err.Error())
			return
		}
		fileData, _ := io.ReadAll(io.LimitReader(tr, 256<<20))
		fileName := filepath.Base(filePath)
		resp.Header().Set("Content-Type", "application/octet-stream")
		resp.Header().Set("Content-Disposition", "attachment; filename=\""+fileName+"\"")
		_, _ = resp.ResponseWriter.Write(fileData)
		return
	}
	httputil.InternalError(resp, "未找到文件")
}

// ─── K8s AI analysis (SSE) ──────────────────────────────────────────────────────────────
type k8sAIAnalyzeReq struct {
	ResourceKind string `json:"resource_kind"` // Pod / Deployment / DaemonSet
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	AnalysisKind string `json:"analysis_kind"` // "logs" or "events"
	Content      string `json:"content"`       // raw text (logs or formatted events)
}

func (h *k8sMgmtHandler) k8sAIAnalyze(req *restful.Request, resp *restful.Response) {
	var body k8sAIAnalyzeReq
	if err := json.NewDecoder(req.Request.Body).Decode(&body); err != nil {
		httputil.BadRequest(resp, "invalid request body: "+err.Error())
		return
	}
	if body.Content == "" {
		httputil.BadRequest(resp, "content is required")
		return
	}

	w := resp.ResponseWriter
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)
	sendChunk := func(text string) {
		// Escape newlines for SSE: replace actual newlines with \n literal so
		// the browser EventSource doesn't split into multiple events.
		escaped := strings.ReplaceAll(text, "\n", "\\n")
		fmt.Fprintf(w, "data: %s\n\n", escaped)
		if canFlush {
			flusher.Flush()
		}
	}

	aReq := ai_pkg.AnalyzeK8sRequest{
		ResourceKind: body.ResourceKind,
		Namespace:    body.Namespace,
		Name:         body.Name,
		AnalysisKind: ai_pkg.K8sAnalysisKind(body.AnalysisKind),
		Content:      body.Content,
	}

	if err := h.agent.AnalyzeK8s(req.Request.Context(), aReq, sendChunk); err != nil {
		sendChunk("[ERROR] " + err.Error())
	}
	// SSE end-of-stream sentinel
	fmt.Fprintf(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
}


// ─── Rollback common types and helpers ──────────────────────────────────────────

type historyEntry struct {
	Revision          string          `json:"revision"`
	Name              string          `json:"name"`
	CreationTimestamp string          `json:"creationTimestamp"`
	Replicas          int             `json:"replicas,omitempty"`
	ReadyReplicas     int             `json:"readyReplicas,omitempty"`
	Template          json.RawMessage `json:"template"`
}

type ownerRef struct {
	Kind string `json:"kind"`
	Name string `json:"kind"`
}

// getSelectorLabels fetches a workload resource and extracts spec.selector.matchLabels.
func (h *k8sMgmtHandler) getSelectorLabels(ctx context.Context, client *http.Client, baseURL, token, path string) map[string]string {
	data, _, _ := h.k8sWriteReq(ctx, client, baseURL, token, http.MethodGet, path, "", nil)
	var obj struct {
		Spec struct {
			Selector struct {
				MatchLabels map[string]string `json:"matchLabels"`
			} `json:"selector"`
		} `json:"spec"`
	}
	_ = json.Unmarshal(data, &obj)
	return obj.Spec.Selector.MatchLabels
}

// labelSelectorStr converts a map to a K8s label selector string.
func labelSelectorStr(labels map[string]string) string {
	var parts []string
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

// listOwnedHistory is a generic rollback history fetcher.
// For Deployment: childAPI = ReplicaSet path, matchBy = annotation "deployment.kubernetes.io/revision"
// For DaemonSet:  childAPI = ControllerRevision path, matchBy = .revision field
func (h *k8sMgmtHandler) listOwnedHistory(
	ctx context.Context, client *http.Client, baseURL, token string,
	ownerKind, ownerName, childAPI string,
	matchOwner func(owners []ownerRef) bool,
	extractRevision func(raw json.RawMessage) (string, string, json.RawMessage), // revision, name, template
) ([]historyEntry, error) {
	data, status, err := h.k8sWriteReq(ctx, client, baseURL, token, http.MethodGet, childAPI, "", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("Kubernetes API error (%d): %s", status, string(data))
	}

	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}

	var history []historyEntry
	for _, raw := range list.Items {
		rev, name, template := extractRevision(raw)
		if rev == "" {
			continue
		}
		history = append(history, historyEntry{
			Revision: rev,
			Name:     name,
			Template: template,
		})
	}

	sort.Slice(history, func(i, j int) bool {
		ri, _ := strconv.Atoi(history[i].Revision)
		rj, _ := strconv.Atoi(history[j].Revision)
		return ri > rj
	})
	return history, nil
}

// findTemplateByRevision scans history entries and returns the template for the matching revision.
func findTemplateByRevision(history []historyEntry, revision string) json.RawMessage {
	for _, e := range history {
		if e.Revision == revision {
			return e.Template
		}
	}
	return nil
}

// patchWorkloadTemplate patches a workload's pod template and writes the response.
func (h *k8sMgmtHandler) patchWorkloadTemplate(
	req *restful.Request, resp *restful.Response,
	targetPath string, template json.RawMessage,
) {
	patch, _ := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": template,
		},
	})
	h.doWrite(req, resp, http.MethodPatch, targetPath,
		"application/merge-patch+json", patch)
}

// ─── Deployment rollback ────────────────────────────────────────────────────

// listDeploymentHistory returns the ReplicaSets owned by the given Deployment,
// sorted by revision (deployment.kubernetes.io/revision annotation).
func (h *k8sMgmtHandler) listDeploymentHistory(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	dsID := req.QueryParameter("ds")
	if dsID == "" {
		httputil.BadRequest(resp, "缺少 ?ds= 参数")
		return
	}
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	ctx := req.Request.Context()
	labels := h.getSelectorLabels(ctx, client, baseURL, token,
		"/apis/apps/v1/namespaces/"+ns+"/deployments/"+name)
	rsPath := "/apis/apps/v1/namespaces/" + ns + "/replicasets"
	if ls := labelSelectorStr(labels); ls != "" {
		rsPath += "?labelSelector=" + url.QueryEscape(ls)
	}

	history, err := h.listOwnedHistory(ctx, client, baseURL, token,
		"Deployment", name, rsPath,
		func(owners []ownerRef) bool {
			for _, ref := range owners {
				if ref.Kind == "Deployment" && ref.Name == name {
					return true
				}
			}
			return false
		},
		func(raw json.RawMessage) (string, string, json.RawMessage) {
			var rs struct {
				Metadata struct {
					Name            string            `json:"name"`
					Annotations     map[string]string `json:"annotations"`
					OwnerReferences []ownerRef        `json:"ownerReferences"`
				} `json:"metadata"`
				Spec struct {
					Replicas int             `json:"replicas"`
					Template json.RawMessage `json:"template"`
				} `json:"spec"`
				Status struct {
					Replicas      int `json:"replicas"`
					ReadyReplicas int `json:"readyReplicas"`
				} `json:"status"`
			}
			if json.Unmarshal(raw, &rs) != nil {
				return "", "", nil
			}
			owned := false
			for _, ref := range rs.Metadata.OwnerReferences {
				if ref.Kind == "Deployment" && ref.Name == name {
					owned = true
					break
				}
			}
			if !owned {
				return "", "", nil
			}
			return rs.Metadata.Annotations["deployment.kubernetes.io/revision"], rs.Metadata.Name, rs.Spec.Template
		},
	)
	if err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}

	// Enrich with replica info
	type rsFull struct {
		Metadata struct {
			Name            string            `json:"name"`
			Annotations     map[string]string `json:"annotations"`
			OwnerReferences []ownerRef        `json:"ownerReferences"`
		} `json:"metadata"`
		Spec struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			Replicas      int `json:"replicas"`
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	}
	// Re-fetch to get replica counts (historyEntry doesn't have them yet)
	var enriched []historyEntry
	data, status, _ := h.k8sWriteReq(ctx, client, baseURL, token, http.MethodGet, rsPath, "", nil)
	if status < 400 {
		var list struct{ Items []json.RawMessage `json:"items"` }
		if json.Unmarshal(data, &list) == nil {
			for _, raw := range list.Items {
				var rs rsFull
				if json.Unmarshal(raw, &rs) != nil {
					continue
				}
				owned := false
				for _, ref := range rs.Metadata.OwnerReferences {
					if ref.Kind == "Deployment" && ref.Name == name {
						owned = true
						break
					}
				}
				if !owned {
					continue
				}
				rev := rs.Metadata.Annotations["deployment.kubernetes.io/revision"]
				enriched = append(enriched, historyEntry{
					Revision:          rev,
					Name:              rs.Metadata.Name,
					Replicas:          rs.Spec.Replicas,
					ReadyReplicas:     rs.Status.ReadyReplicas,
					CreationTimestamp: "",
				})
			}
		}
	}
	if enriched != nil {
		httputil.Success(resp, enriched)
	} else {
		httputil.Success(resp, history)
	}
}

// rollbackDeployment rolls back a Deployment to the specified revision by
// patching its pod template with the template from the target ReplicaSet.
func (h *k8sMgmtHandler) rollbackDeployment(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	revision := req.QueryParameter("revision")
	if revision == "" {
		httputil.BadRequest(resp, "缺少 revision 参数")
		return
	}
	dsID := req.QueryParameter("ds")
	if dsID == "" {
		httputil.BadRequest(resp, "缺少 ?ds= 参数")
		return
	}
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	ctx := req.Request.Context()
	labels := h.getSelectorLabels(ctx, client, baseURL, token,
		"/apis/apps/v1/namespaces/"+ns+"/deployments/"+name)
	rsPath := "/apis/apps/v1/namespaces/" + ns + "/replicasets"
	if ls := labelSelectorStr(labels); ls != "" {
		rsPath += "?labelSelector=" + url.QueryEscape(ls)
	}

	history, err := h.listOwnedHistory(ctx, client, baseURL, token,
		"Deployment", name, rsPath,
		func(owners []ownerRef) bool {
			for _, ref := range owners {
				if ref.Kind == "Deployment" && ref.Name == name {
					return true
				}
			}
			return false
		},
		func(raw json.RawMessage) (string, string, json.RawMessage) {
			var rs struct {
				Metadata struct {
					Annotations     map[string]string `json:"annotations"`
					OwnerReferences []ownerRef        `json:"ownerReferences"`
				} `json:"metadata"`
				Spec struct {
					Template json.RawMessage `json:"template"`
				} `json:"spec"`
			}
			if json.Unmarshal(raw, &rs) != nil {
				return "", "", nil
			}
			owned := false
			for _, ref := range rs.Metadata.OwnerReferences {
				if ref.Kind == "Deployment" && ref.Name == name {
					owned = true
					break
				}
			}
			if !owned {
				return "", "", nil
			}
			return rs.Metadata.Annotations["deployment.kubernetes.io/revision"], "", rs.Spec.Template
		},
	)
	if err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}

	template := findTemplateByRevision(history, revision)
	if template == nil {
		httputil.Error(resp, http.StatusNotFound, fmt.Sprintf("revision %s not found for deployment %s", revision, name))
		return
	}
	h.patchWorkloadTemplate(req, resp,
		"/apis/apps/v1/namespaces/"+ns+"/deployments/"+name, template)
}

// ─── DaemonSet rollback ───────────────────────────────────────────────────────

// listDaemonSetHistory returns ControllerRevisions owned by the given DaemonSet,
// sorted by revision number descending.
func (h *k8sMgmtHandler) listDaemonSetHistory(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	dsID := req.QueryParameter("ds")
	if dsID == "" {
		httputil.BadRequest(resp, "缺少 ?ds= 参数")
		return
	}
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	ctx := req.Request.Context()
	labels := h.getSelectorLabels(ctx, client, baseURL, token,
		"/apis/apps/v1/namespaces/"+ns+"/daemonsets/"+name)
	crPath := "/apis/apps/v1/namespaces/" + ns + "/controllerrevisions"
	if ls := labelSelectorStr(labels); ls != "" {
		crPath += "?labelSelector=" + url.QueryEscape(ls)
	}

	history, err := h.listOwnedHistory(ctx, client, baseURL, token,
		"DaemonSet", name, crPath,
		func(owners []ownerRef) bool {
			for _, ref := range owners {
				if ref.Kind == "DaemonSet" && ref.Name == name {
					return true
				}
			}
			return false
		},
		func(raw json.RawMessage) (string, string, json.RawMessage) {
			var cr struct {
				Metadata struct {
					Name              string     `json:"name"`
					CreationTimestamp string     `json:"creationTimestamp"`
					OwnerReferences   []ownerRef `json:"ownerReferences"`
				} `json:"metadata"`
				Revision int64           `json:"revision"`
				Data     json.RawMessage `json:"data"`
			}
			if json.Unmarshal(raw, &cr) != nil {
				return "", "", nil
			}
			owned := false
			for _, ref := range cr.Metadata.OwnerReferences {
				if ref.Kind == "DaemonSet" && ref.Name == name {
					owned = true
					break
				}
			}
			if !owned {
				return "", "", nil
			}
			var crData struct {
				Spec struct {
					Template json.RawMessage `json:"template"`
				} `json:"spec"`
			}
			_ = json.Unmarshal(cr.Data, &crData)
			return strconv.FormatInt(cr.Revision, 10), cr.Metadata.Name, crData.Spec.Template
		},
	)
	if err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}
	httputil.Success(resp, history)
}

// rollbackDaemonSet rolls back a DaemonSet to the specified revision by
// patching its pod template with the template from the target ControllerRevision.
func (h *k8sMgmtHandler) rollbackDaemonSet(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	revision := req.QueryParameter("revision")
	if revision == "" {
		httputil.BadRequest(resp, "缺少 revision 参数")
		return
	}
	dsID := req.QueryParameter("ds")
	if dsID == "" {
		httputil.BadRequest(resp, "缺少 ?ds= 参数")
		return
	}
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}

	ctx := req.Request.Context()
	labels := h.getSelectorLabels(ctx, client, baseURL, token,
		"/apis/apps/v1/namespaces/"+ns+"/daemonsets/"+name)
	crPath := "/apis/apps/v1/namespaces/" + ns + "/controllerrevisions"
	if ls := labelSelectorStr(labels); ls != "" {
		crPath += "?labelSelector=" + url.QueryEscape(ls)
	}

	history, err := h.listOwnedHistory(ctx, client, baseURL, token,
		"DaemonSet", name, crPath,
		func(owners []ownerRef) bool {
			for _, ref := range owners {
				if ref.Kind == "DaemonSet" && ref.Name == name {
					return true
				}
			}
			return false
		},
		func(raw json.RawMessage) (string, string, json.RawMessage) {
			var cr struct {
				Metadata struct {
					OwnerReferences []ownerRef `json:"ownerReferences"`
				} `json:"metadata"`
				Revision int64           `json:"revision"`
				Data     json.RawMessage `json:"data"`
			}
			if json.Unmarshal(raw, &cr) != nil {
				return "", "", nil
			}
			owned := false
			for _, ref := range cr.Metadata.OwnerReferences {
				if ref.Kind == "DaemonSet" && ref.Name == name {
					owned = true
					break
				}
			}
			if !owned {
				return "", "", nil
			}
			var crData struct {
				Spec struct {
					Template json.RawMessage `json:"template"`
				} `json:"spec"`
			}
			_ = json.Unmarshal(cr.Data, &crData)
			return strconv.FormatInt(cr.Revision, 10), "", crData.Spec.Template
		},
	)
	if err != nil {
		httputil.InternalError(resp, err.Error())
		return
	}

	template := findTemplateByRevision(history, revision)
	if template == nil {
		httputil.Error(resp, http.StatusNotFound, fmt.Sprintf("revision %s not found for daemonset %s", revision, name))
		return
	}
	h.patchWorkloadTemplate(req, resp,
		"/apis/apps/v1/namespaces/"+ns+"/daemonsets/"+name, template)
}
func (h *k8sMgmtHandler) podDescribe(req *restful.Request, resp *restful.Response) {
	ns, name, ok := nsName(req)
	if !ok {
		httputil.BadRequest(resp, "缺少 namespace 或 name 参数")
		return
	}
	h.doGet(req, resp, "/api/v1/namespaces/"+ns+"/pods/"+name)
}

// ─── podMetrics: proxy metrics-server for pod CPU/memory ──────────────────────
func (h *k8sMgmtHandler) podMetrics(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	ns := req.QueryParameter("namespace")
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}
	var path string
	if ns != "" {
		path = "/apis/metrics.k8s.io/v1beta1/namespaces/" + ns + "/pods"
	} else {
		path = "/apis/metrics.k8s.io/v1beta1/pods"
	}
	k8sReq, _ := http.NewRequestWithContext(req.Request.Context(), http.MethodGet, baseURL+path, nil)
	if token != "" {
		k8sReq.Header.Set("Authorization", "Bearer "+token)
	}
	k8sReq.Header.Set("Accept", "application/json")
	k8sResp, err := client.Do(k8sReq)
	if err != nil {
		httputil.InternalError(resp, "metrics-server 请求失败: "+err.Error())
		return
	}
	defer func() { _ = k8sResp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(k8sResp.Body, 8*1024*1024))
	if k8sResp.StatusCode >= 400 {
		// metrics-server 不可用时返回空列表而不报错，前端降级展示
		httputil.Success(resp, map[string]any{"items": []any{}})
		return
	}
	var result any
	if e := json.Unmarshal(body, &result); e != nil {
		httputil.Success(resp, map[string]any{"items": []any{}})
		return
	}
	httputil.Success(resp, result)
}

// ─── globalEvents: list events across all/specific namespace ──────────────────
func (h *k8sMgmtHandler) globalEvents(req *restful.Request, resp *restful.Response) {
	dsID := req.QueryParameter("ds")
	ns := req.QueryParameter("namespace")
	eventType := req.QueryParameter("type") // Warning | Normal | ""
	client, baseURL, token, err := h.k8sClient(dsID)
	if err != nil {
		httputil.BadRequest(resp, err.Error())
		return
	}
	q := url.Values{}
	if eventType != "" {
		q.Set("fieldSelector", "type="+eventType)
	}
	var fullURL string
	if ns != "" {
		fullURL = baseURL + "/api/v1/namespaces/" + ns + "/events"
	} else {
		fullURL = baseURL + "/api/v1/events"
	}
	if len(q) > 0 {
		fullURL += "?" + q.Encode()
	}
	k8sReq, _ := http.NewRequestWithContext(req.Request.Context(), http.MethodGet, fullURL, nil)
	if token != "" {
		k8sReq.Header.Set("Authorization", "Bearer "+token)
	}
	k8sReq.Header.Set("Accept", "application/json")
	k8sResp, err := client.Do(k8sReq)
	if err != nil {
		httputil.InternalError(resp, "Kubernetes API 请求失败: "+err.Error())
		return
	}
	defer func() { _ = k8sResp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(k8sResp.Body, 16*1024*1024))
	if k8sResp.StatusCode >= 400 {
		httputil.Error(resp, k8sResp.StatusCode, fmt.Sprintf("Kubernetes API error: %s", string(body)))
		return
	}
	var result any
	if e := json.Unmarshal(body, &result); e != nil {
		httputil.InternalError(resp, fmt.Sprintf("解析响应失败: %v", e))
		return
	}
	httputil.Success(resp, result)
}
