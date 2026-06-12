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
		go spec.inf.Run(cc.stopCh)
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

		allSynced := true
		for _, spec := range infs {
			if !cache.WaitForCacheSync(ctx.Done(), spec.inf.HasSynced) {
				allSynced = false
				log.Warn().Str("ds", cc.dsID).Str("resource", string(spec.res)).Msg("informer sync timeout")
			}
		}
		if allSynced {
			cc.mu.Lock()
			cc.ready = true
			cc.mu.Unlock()
			log.Info().Str("ds", cc.dsID).Str("name", cc.name).Msg("k8s cache synced and ready")
		}
		close(cc.readyCh)
	}()

	factory.Start(cc.stopCh)
}

func (cc *ClusterCache) startDynamicInformerWithFallback(res ResourceType, gvrs []schema.GroupVersionResource) {
	for _, gvr := range gvrs {
		// 每个版本使用独立的 factory，避免 GVR 间互相干扰
		dynFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(cc.dynCli, 5*time.Minute, "", nil)
		inf := dynFactory.ForResource(gvr).Informer()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		dynFactory.Start(ctx.Done()) // 启动 factory 内所有已注册的 informer

		synced := cache.WaitForCacheSync(ctx.Done(), inf.HasSynced)
		cancel() // 立即释放 context，而非 defer

		if synced {
			cc.mu.Lock()
			cc.stores[res] = inf.GetStore()
			cc.mu.Unlock()
			log.Info().Str("ds", cc.dsID).Str("resource", string(res)).Str("gvr", gvr.Group+"/"+gvr.Version).Msg("dynamic informer synced")
			return
		}
		// 当前版本不可用，清理后尝试下一版本
		log.Warn().Str("ds", cc.dsID).Str("resource", string(res)).Str("gvr", gvr.Group+"/"+gvr.Version).Msg("dynamic informer version not available, trying next")
	}

	// All versions failed
	log.Warn().Str("ds", cc.dsID).Str("resource", string(res)).Msg("all dynamic informer versions failed")
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
	Items            []map[string]any `json:"items"`
	Total            int              `json:"total"`
	Page             int              `json:"page"`
	PageSize         int              `json:"pageSize"`
	AvailableStatuses []string        `json:"availableStatuses,omitempty"`
}

func paginate(items []map[string]any, page, pageSize int) PaginateResult {
	total := len(items)
	if page < 1 {
		page = 1
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
