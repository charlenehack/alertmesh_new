package k8scache

import (
	"encoding/json"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

type SearchParams struct {
	Search    string
	Namespace string
	Page      int
	PageSize  int
	Phase     string
	NodeName  string
	Ready     string
	LabelSelector string
}

func (cc *ClusterCache) SearchPods(params SearchParams) PaginateResult {
	all := cc.ListPodsRaw()

	// 先收集所有派生状态（从全量缓存计算）
	statusSet := make(map[string]struct{})
	for _, pod := range all {
		s := podDerivedStatus(pod)
		statusSet[s] = struct{}{}
	}
	availableStatuses := make([]string, 0, len(statusSet))
	for s := range statusSet {
		availableStatuses = append(availableStatuses, s)
	}

	// 再过滤
	filtered := make([]map[string]any, 0, len(all))
	for _, pod := range all {
		if !matchesNamespace(pod.Namespace, params.Namespace) {
			continue
		}
		if !matchesSearch(pod.Name, params.Search) {
			continue
		}
		if params.Phase != "" && string(pod.Status.Phase) != params.Phase {
			continue
		}
		if params.NodeName != "" && !strings.Contains(
			strings.ToLower(pod.Spec.NodeName),
			strings.ToLower(params.NodeName),
		) {
			continue
		}
		if params.Ready != "" {
			total := len(pod.Spec.Containers)
			ready := 0
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.Ready {
					ready++
				}
			}
			if params.Ready == "ready" && ready != total {
				continue
			}
			if params.Ready == "notready" && ready == total {
				continue
			}
		}
		if !matchesLabels(pod.Labels, params.LabelSelector) {
			continue
		}
		filtered = append(filtered, toJSON(pod))
	}

	sortByCreation(filtered)
	result := paginate(filtered, params.Page, params.PageSize)
	result.AvailableStatuses = availableStatuses
	return result
}

// podDerivedStatus computes a human-readable status from container states.
// Matches the frontend podDerivedStatus logic.
func podDerivedStatus(pod *corev1.Pod) string {
	if pod.DeletionTimestamp != nil {
		return "Terminating"
	}
	phase := string(pod.Status.Phase)
	if phase == "Failed" && pod.Status.Reason == "Evicted" {
		return "Evicted"
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" && cs.State.Terminated.Reason != "Completed" {
			return cs.State.Terminated.Reason
		}
	}
	return phase
}

func (cc *ClusterCache) SearchNodes(params SearchParams) PaginateResult {
	all := cc.ListNodesRaw()
	filtered := make([]map[string]any, 0, len(all))

	for _, node := range all {
		if !matchesSearch(node.Name, params.Search) {
			continue
		}
		filtered = append(filtered, toJSON(node))
	}

	sortByCreation(filtered)
	return paginate(filtered, params.Page, params.PageSize)
}

func (cc *ClusterCache) SearchGeneric(res ResourceType, params SearchParams) PaginateResult {
	items := cc.ListStore(res)
	filtered := make([]map[string]any, 0, len(items))

	for _, obj := range items {
		// Use JSON round-trip to get map[string]any
		data, err := json.Marshal(obj)
		if err != nil {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}

		meta, _ := m["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		ns, _ := meta["namespace"].(string)

		if !matchesNamespace(ns, params.Namespace) {
			continue
		}
		if !matchesSearch(name, params.Search) {
			continue
		}
		filtered = append(filtered, m)
	}

	sortByCreation(filtered)
	return paginate(filtered, params.Page, params.PageSize)
}

func matchesLabels(labels map[string]string, selector string) bool {
	if selector == "" {
		return true
	}
	pairs := strings.Split(selector, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		if labels[strings.TrimSpace(kv[0])] != strings.TrimSpace(kv[1]) {
			return false
		}
	}
	return true
}

func sortByCreation(items []map[string]any) {
	sort.Slice(items, func(i, j int) bool {
		ti := creationTime(items[i])
		tj := creationTime(items[j])
		return ti > tj
	})
}

func creationTime(m map[string]any) string {
	meta, ok := m["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	ts, _ := meta["creationTimestamp"].(string)
	return ts
}

// CountStore returns the total number of cached items for a resource type.
func (cc *ClusterCache) CountStore(res ResourceType) int {
	return len(cc.ListStore(res))
}

// Ensure corev1 import is used.
var _ corev1.Pod
