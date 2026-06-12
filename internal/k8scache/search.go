package k8scache

import (
	"encoding/json"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

type SearchParams struct {
	Search        string
	Namespace     string
	Page          int
	PageSize      int
	Phase         string
	NodeName      string
	Ready         string
	LabelSelector string
	ClusterIP     string
	Hosts         string
}

func (cc *ClusterCache) SearchPods(params SearchParams) PaginateResult {
	all := cc.ListPodsRaw()

	// 第一遍过滤：除 phase 外的所有条件
	// 同时收集可用状态（不受 phase 过滤影响，确保用户可切换状态）
	type podEntry struct {
		obj    map[string]any
		status string
	}
	preFiltered := make([]podEntry, 0, len(all))
	statusSet := make(map[string]struct{})
	for _, pod := range all {
		if !matchesNamespace(pod.Namespace, params.Namespace) {
			continue
		}
		if !matchesSearch(pod.Name, params.Search) {
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
		s := podDerivedStatus(pod)
		statusSet[s] = struct{}{}
		preFiltered = append(preFiltered, podEntry{obj: toJSON(pod), status: s})
	}

	availableStatuses := make([]string, 0, len(statusSet))
	for s := range statusSet {
		availableStatuses = append(availableStatuses, s)
	}

	// 第二遍过滤：应用 phase 过滤
	filtered := make([]map[string]any, 0, len(preFiltered))
	for _, entry := range preFiltered {
		if params.Phase != "" && entry.status != params.Phase {
			continue
		}
		filtered = append(filtered, entry.obj)
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

		// Services: filter by ClusterIP
		if params.ClusterIP != "" {
			spec, _ := m["spec"].(map[string]any)
			clusterIP, _ := spec["clusterIP"].(string)
			if !strings.Contains(strings.ToLower(clusterIP), strings.ToLower(params.ClusterIP)) {
				continue
			}
		}

		// Ingresses: filter by Hosts
		if params.Hosts != "" {
			spec, _ := m["spec"].(map[string]any)
			rules, _ := spec["rules"].([]any)
			matched := false
			for _, r := range rules {
				rule, _ := r.(map[string]any)
				host, _ := rule["host"].(string)
				if strings.Contains(strings.ToLower(host), strings.ToLower(params.Hosts)) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
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
