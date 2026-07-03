package k8scache

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

type ResourceType string

const (
	ResPods         ResourceType = "pods"
	ResDeployments  ResourceType = "deployments"
	ResDaemonSets   ResourceType = "daemonsets"
	ResStatefulSets ResourceType = "statefulsets"
	ResConfigMaps   ResourceType = "configmaps"
	ResServices     ResourceType = "services"
	ResIngresses    ResourceType = "ingresses"
	ResNodes        ResourceType = "nodes"
	ResPVCs         ResourceType = "pvcs"
	ResHPAs         ResourceType = "hpas"
	ResEndpoints    ResourceType = "endpoints"
)

// ClusterCache holds Informer-backed stores for one K8s cluster.
type ClusterCache struct {
	dsID   string
	name   string
	client kubernetes.Interface
	dynCli dynamic.Interface

	mu     sync.RWMutex
	stores map[ResourceType]cache.Store
	stopCh chan struct{}

	ready   bool
	readyCh chan struct{}
}

type ClusterConfig struct {
	BaseURL               string
	Token                 string
	InCluster             bool
	TLSInsecureSkipVerify bool
}

func NewClusterCache(dsID, name string, cfg ClusterConfig) (*ClusterCache, error) {
	restCfg, err := buildRestConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build rest config: %w", err)
	}

	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	dynCli, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}

	cc := &ClusterCache{
		dsID:    dsID,
		name:    name,
		client:  client,
		dynCli:  dynCli,
		stores:  make(map[ResourceType]cache.Store),
		stopCh:  make(chan struct{}),
		readyCh: make(chan struct{}),
	}

	cc.startInformers()
	return cc, nil
}

func buildRestConfig(cfg ClusterConfig) (*rest.Config, error) {
	if cfg.InCluster {
		return rest.InClusterConfig()
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("empty API server URL")
	}
	return &rest.Config{
		Host:        cfg.BaseURL,
		BearerToken: cfg.Token,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: cfg.TLSInsecureSkipVerify,
		},
		Timeout: 30 * time.Second,
	}, nil
}

func (cc *ClusterCache) startInformers() {
	factory := informers.NewSharedInformerFactory(cc.client, 5*time.Minute)

	type informerSpec struct {
		res ResourceType
		inf cache.SharedInformer
	}

	infs := []informerSpec{
		{ResPods, factory.Core().V1().Pods().Informer()},
		{ResNodes, factory.Core().V1().Nodes().Informer()},
		{ResConfigMaps, factory.Core().V1().ConfigMaps().Informer()},
		{ResServices, factory.Core().V1().Services().Informer()},
		{ResEndpoints, factory.Core().V1().Endpoints().Informer()},
		{ResPVCs, factory.Core().V1().PersistentVolumeClaims().Informer()},
		{ResDeployments, factory.Apps().V1().Deployments().Informer()},
		{ResDaemonSets, factory.Apps().V1().DaemonSets().Informer()},
		{ResStatefulSets, factory.Apps().V1().StatefulSets().Informer()},
	}

	for _, spec := range infs {
		store := spec.inf.GetStore()
		cc.mu.Lock()
		cc.stores[spec.res] = store
		cc.mu.Unlock()
		spec.inf.AddEventHandler(cache.ResourceEventHandlerFuncs{})
	}

	go cc.startDynamicInformerWithFallback(ResIngresses, []schema.GroupVersionResource{
		{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
		{Group: "networking.k8s.io", Version: "v1beta1", Resource: "ingresses"},
		{Group: "extensions", Version: "v1beta1", Resource: "ingresses"},
	})
	go cc.startDynamicInformerWithFallback(ResHPAs, []schema.GroupVersionResource{
		{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"},
		{Group: "autoscaling", Version: "v2beta2", Resource: "horizontalpodautoscalers"},
		{Group: "autoscaling", Version: "v1", Resource: "horizontalpodautoscalers"},
	})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// 并行等待所有 informer 同步完成
		hasSyncedFuncs := make([]cache.InformerSynced, len(infs))
		for i, spec := range infs {
			hasSyncedFuncs[i] = spec.inf.HasSynced
		}
		if cache.WaitForCacheSync(ctx.Done(), hasSyncedFuncs...) {
			cc.mu.Lock()
			cc.ready = true
			cc.mu.Unlock()
			log.Info().Str("ds", cc.dsID).Str("name", cc.name).Msg("k8s cache synced and ready")
		} else {
			// 部分 informer 超时（如某些资源不存在），检查核心资源是否就绪
			coreReady := 0
			for _, spec := range infs {
				if spec.inf.HasSynced() {
					if spec.res == ResPods || spec.res == ResDeployments {
						coreReady++
					}
					log.Info().Str("ds", cc.dsID).Str("resource", string(spec.res)).Msg("informer synced")
				} else {
					log.Warn().Str("ds", cc.dsID).Str("resource", string(spec.res)).Msg("informer sync timeout")
				}
			}
			if coreReady >= 2 {
				cc.mu.Lock()
				cc.ready = true
				cc.mu.Unlock()
				log.Info().Str("ds", cc.dsID).Str("name", cc.name).Msg("k8s cache ready (core resources synced)")
			} else {
				log.Warn().Str("ds", cc.dsID).Str("name", cc.name).Int("coreReady", coreReady).
					Msg("k8s cache not ready: core resources failed to sync")
			}
		}
		close(cc.readyCh)
	}()

	factory.Start(cc.stopCh)
}

func (cc *ClusterCache) startDynamicInformerWithFallback(res ResourceType, gvrs []schema.GroupVersionResource) {
	for _, gvr := range gvrs {
		// 用独立的 factory 探测版本可用性（30s 超时）
		probeFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(cc.dynCli, 5*time.Minute, "", nil)
		probeInf := probeFactory.ForResource(gvr).Informer()

		probeCtx, probeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		probeFactory.Start(probeCtx.Done())
		synced := cache.WaitForCacheSync(probeCtx.Done(), probeInf.HasSynced)
		probeCancel()

		if !synced {
			// 该版本不可用，尝试下一版本
			log.Warn().Str("ds", cc.dsID).Str("resource", string(res)).
				Str("gvr", gvr.Group+"/"+gvr.Version).
				Msg("dynamic informer version not available, trying next")
			continue
		}

		// 版本可用：用 cc.stopCh 启动长期运行的 factory，保持 store 持续同步
		liveFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(cc.dynCli, 5*time.Minute, "", nil)
		liveInf := liveFactory.ForResource(gvr).Informer()
		liveFactory.Start(cc.stopCh)

		// 等待长期 informer 完成初次同步（最多 2 分钟）
		syncCtx, syncCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		cache.WaitForCacheSync(syncCtx.Done(), liveInf.HasSynced)
		syncCancel()

		cc.mu.Lock()
		cc.stores[res] = liveInf.GetStore()
		cc.mu.Unlock()

		log.Info().Str("ds", cc.dsID).Str("resource", string(res)).
			Str("gvr", gvr.Group+"/"+gvr.Version).
			Msg("dynamic informer synced and running")
		return
	}

	// 所有版本均不可用
	log.Warn().Str("ds", cc.dsID).Str("resource", string(res)).
		Msg("all dynamic informer versions failed")
}

func (cc *ClusterCache) Stop() {
	close(cc.stopCh)
	log.Info().Str("ds", cc.dsID).Str("name", cc.name).Msg("k8s cache stopped")
}

func (cc *ClusterCache) Ready() bool {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return cc.ready
}

func (cc *ClusterCache) ReadyCh() <-chan struct{} {
	return cc.readyCh
}

func (cc *ClusterCache) ListStore(res ResourceType) []any {
	cc.mu.RLock()
	store, ok := cc.stores[res]
	cc.mu.RUnlock()
	if !ok {
		return nil
	}
	return store.List()
}

func (cc *ClusterCache) ListPodsRaw() []*corev1.Pod {
	items := cc.ListStore(ResPods)
	pods := make([]*corev1.Pod, 0, len(items))
	for _, obj := range items {
		if pod, ok := obj.(*corev1.Pod); ok {
			pods = append(pods, pod)
		}
	}
	return pods
}

func (cc *ClusterCache) ListNodesRaw() []*corev1.Node {
	items := cc.ListStore(ResNodes)
	nodes := make([]*corev1.Node, 0, len(items))
	for _, obj := range items {
		if node, ok := obj.(*corev1.Node); ok {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// PodCountsPerNode returns the number of running/non-terminating pods
// scheduled on each node, keyed by node name.
func (cc *ClusterCache) PodCountsPerNode() map[string]int {
	counts := make(map[string]int)
	for _, pod := range cc.ListPodsRaw() {
		if pod.Spec.NodeName == "" {
			continue
		}
		// Ignore pods that are being deleted or already succeeded/failed and not terminating
		phase := string(pod.Status.Phase)
		if pod.DeletionTimestamp != nil || phase == "Succeeded" || phase == "Failed" {
			continue
		}
		counts[pod.Spec.NodeName]++
	}
	return counts
}

// PodResourceSummary returns the total number of non-terminated pods and the
// aggregate CPU/memory requests/limits across all containers.
// Pods in Succeeded or Failed phases are excluded to match kubectl semantics.
func (cc *ClusterCache) PodResourceSummary() (totalPods int, reqCPUm, reqMemKi, limCPUm, limMemKi int64) {
	for _, pod := range cc.ListPodsRaw() {
		phase := string(pod.Status.Phase)
		if phase == "Succeeded" || phase == "Failed" {
			continue
		}
		totalPods++
		for _, c := range pod.Spec.Containers {
			if cpu := c.Resources.Requests.Cpu(); cpu != nil {
				reqCPUm += cpu.MilliValue()
			}
			if mem := c.Resources.Requests.Memory(); mem != nil {
				reqMemKi += mem.Value() / 1024
			}
			if cpu := c.Resources.Limits.Cpu(); cpu != nil {
				limCPUm += cpu.MilliValue()
			}
			if mem := c.Resources.Limits.Memory(); mem != nil {
				limMemKi += mem.Value() / 1024
			}
		}
	}
	return
}

func toJSON(obj any) map[string]any {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return out
}

func matchesSearch(name, search string) bool {
	if search == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), strings.ToLower(search))
}

func matchesNamespace(objNS, filterNS string) bool {
	if filterNS == "" {
		return true
	}
	return objNS == filterNS
}

type PaginateResult struct {
	Items             []map[string]any `json:"items"`
	Total             int              `json:"total"`
	Page              int              `json:"page"`
	PageSize          int              `json:"pageSize"`
	AvailableStatuses []string         `json:"availableStatuses,omitempty"`
}

func paginate(items []map[string]any, page, pageSize int) PaginateResult {
	total := len(items)
	if page < 1 {
		page = 1
	}
	// pageSize=-1 表示全量返回，不分页
	if pageSize == -1 {
		return PaginateResult{Items: items, Total: total, Page: 1, PageSize: total}
	}
	if pageSize < 1 {
		pageSize = 20
	}
	start := (page - 1) * pageSize
	if start >= total {
		return PaginateResult{Items: []map[string]any{}, Total: total, Page: page, PageSize: pageSize}
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return PaginateResult{
		Items:    items[start:end],
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
}

// PodStatusCounts returns a map of derived status → count for all pods.
func (cc *ClusterCache) PodStatusCounts() map[string]int {
	counts := make(map[string]int)
	for _, pod := range cc.ListPodsRaw() {
		s := podDerivedStatus(pod)
		counts[s]++
	}
	return counts
}

func buildHTTPTransport(tlsSkip bool) *http.Transport {
	return &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: tlsSkip},
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}
}
