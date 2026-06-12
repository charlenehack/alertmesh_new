/**
 * Shared hook for K8s pages – provides the selected cluster ID
 * (persisted in sessionStorage so page refreshes keep the selection).
 */
import { useState, useEffect, useRef, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { http } from '../../api/request'

export interface ClusterRow {
  id: string
  name: string
  description: string
  endpoint: string
  is_enabled: boolean
  is_default: boolean
  last_test_ok: boolean | null
}

export function useClusters() {
  return useQuery<ClusterRow[]>({
    queryKey: ['k8s-clusters'],
    queryFn: () => http.get<ClusterRow[]>('/k8s/clusters'),
    staleTime: 30_000,
  })
}

/** 获取指定集群的命名空间列表 */
export function useNamespaces(dsId: string) {
  return useQuery<string[]>({
    queryKey: ['k8s-namespaces', dsId],
    queryFn: async () => {
      const res = await http.get<any>('/k8s/namespaces', { params: { ds: dsId } })
      const items: any[] = res?.items ?? []
      return items.map((ns: any) => ns.metadata?.name ?? '').filter(Boolean).sort()
    },
    enabled: !!dsId,
    staleTime: 60_000,
  })
}

/**
 * 自动刷新 hook
 * @param onRefresh 刷新回调
 * @param defaultInterval 默认间隔秒数，0 = 默认关闭
 */
export function useAutoRefresh(onRefresh: () => void, defaultInterval = 0) {
  const [enabled, setEnabled] = useState(defaultInterval > 0)
  const [interval, setInterval_] = useState(defaultInterval || 30)
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  useEffect(() => {
    if (timerRef.current) clearInterval(timerRef.current)
    if (!enabled) return
    timerRef.current = setInterval(onRefresh, interval * 1000)
    return () => { if (timerRef.current) clearInterval(timerRef.current) }
  }, [enabled, interval, onRefresh])

  return { enabled, setEnabled, interval, setInterval: setInterval_ }
}

const SESSION_KEY = 'alertmesh_k8s_cluster'

export function useSelectedCluster(clusters: ClusterRow[] | undefined) {
  const [dsId, setDsId] = useState<string>(() => sessionStorage.getItem(SESSION_KEY) ?? '')

  useEffect(() => {
    if (!clusters || clusters.length === 0) return
    // Auto-select: prefer persisted → default → first enabled → first
    const valid = clusters.find(c => c.id === dsId)
    if (valid) return
    const def = clusters.find(c => c.is_default) ?? clusters.find(c => c.is_enabled) ?? clusters[0]
    if (def) {
      setDsId(def.id)
      sessionStorage.setItem(SESSION_KEY, def.id)
    }
  }, [clusters, dsId])

  const select = (id: string) => {
    setDsId(id)
    sessionStorage.setItem(SESSION_KEY, id)
  }

  return { dsId, select }
}

/** Sort K8s resources by creationTimestamp (newest first) */
export function byCreation<T extends { metadata?: { creationTimestamp?: unknown } }>(a: T, b: T): number {
  const ta = String(a.metadata?.creationTimestamp ?? '')
  const tb = String(b.metadata?.creationTimestamp ?? '')
  return tb.localeCompare(ta) // newest first
}

/** Pagination config that doesn't reset on re-renders */
export const k8sPagination = {
  defaultPageSize: 20,
  showSizeChanger: true,
  showTotal: (t: number) => `共 ${t} 个`,
} as const

/** Format creationTimestamp + running duration */
export function fmtCreation(ts: unknown): { date: string; age: string } {
  if (!ts) return { date: '—', age: '—' }
  const d = new Date(String(ts))
  const date = `${(d.getMonth() + 1).toString().padStart(2, '0')}-${d.getDate().toString().padStart(2, '0')} ${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`
  const diffMs = Date.now() - d.getTime()
  if (diffMs < 0) return { date, age: '刚刚' }
  const seconds = Math.floor(diffMs / 1000)
  if (seconds < 60) return { date, age: `${seconds}s` }
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return { date, age: `${minutes}m` }
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return { date, age: `${hours}h${minutes % 60}m` }
  const days = Math.floor(hours / 24)
  if (days < 30) return { date, age: `${days}d${hours % 24}h` }
  const months = Math.floor(days / 30)
  if (months < 12) return { date, age: `${months}mo` }
  return { date, age: `${Math.floor(months / 12)}y` }
}

// ─── Server-side paginated list ─────────────────────────────────────────────

export interface K8sListResponse<T> {
  items: T[]
  total: number
  page: number
  pageSize: number
  availableStatuses?: string[]
}

interface UseK8sListOptions {
  dsId: string
  namespace?: string
  /** Initial label selector (e.g. from URL params) */
  labelSelector?: string
  /** Initial node name filter */
  nodeName?: string
  /** Initial page size */
  pageSize?: number
  /** Debounce delay for search (ms). 0 = no search, direct API call */
  searchDelay?: number
  /** Additional query params to include in API calls (e.g. clusterIP, hosts) */
  extraParams?: Record<string, string>
}

export function useK8sList<T = any>(
  endpoint: string,
  opts: UseK8sListOptions,
) {
  const { dsId, namespace: nsInit, labelSelector: lsInit, nodeName: nnInit, pageSize: pageSizeInit = 20, searchDelay = 300, extraParams } = opts

  const [search, setSearch] = useState('')
  const [phase, setPhase_] = useState('')
  const [nodeName, setNodeName_] = useState(nnInit ?? '')
  const [ready, setReady_] = useState('')
  const [labelSelector, setLabelSelector_] = useState(lsInit ?? '')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(pageSizeInit)
  const [namespace, setNamespace_] = useState(nsInit ?? '')
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // 筛选条件变化时重置到第1页
  const setPhase = (v: string) => { setPhase_(v); setPage(1) }
  const setNodeName = (v: string) => { setNodeName_(v); setPage(1) }
  const setReady = (v: string) => { setReady_(v); setPage(1) }
  const setLabelSelector = (v: string) => { setLabelSelector_(v); setPage(1) }
  const setNamespace = (v: string) => { setNamespace_(v); setPage(1) }

  // Debounced search
  const doSearch = (keyword: string) => {
    if (timerRef.current) clearTimeout(timerRef.current)
    if (searchDelay > 0) {
      timerRef.current = setTimeout(() => {
        setSearch(keyword)
        setPage(1)
      }, searchDelay)
    } else {
      setSearch(keyword)
      setPage(1)
    }
  }

  const params: Record<string, string | number> = {
    ds: dsId,
    page,
    pageSize,
  }
  if (namespace) params.namespace = namespace
  if (search) params.search = search
  if (phase) params.phase = phase
  if (nodeName) params.nodeName = nodeName
  if (ready) params.ready = ready
  if (labelSelector) params.labelSelector = labelSelector
  // 添加额外的查询参数（如 clusterIP、hosts）
  if (extraParams) {
    for (const [k, v] of Object.entries(extraParams)) {
      if (v) params[k] = v
    }
  }

  const queryResult = useQuery<K8sListResponse<T>>({
    queryKey: ['k8s-list', endpoint, dsId, namespace, search, phase, nodeName, ready, labelSelector, page, pageSize, extraParams],
    queryFn: async () => {
      const raw = await http.get<any>(endpoint, { params })
      // 兼容两种响应格式：
      // 1. 缓存可用时返回 K8sListResponse { items, total, page, pageSize }
      // 2. 缓存不可用时 fallback 返回原始 K8s 格式 { kind, items, metadata }
      if (raw && typeof raw.total === 'number') {
        return raw as K8sListResponse<T>
      }
      // 原始 K8s 格式 → 转换为 K8sListResponse
      const items: T[] = raw?.items ?? []
      return {
        items,
        total: items.length,
        page: 1,
        pageSize: items.length || 20,
      } as K8sListResponse<T>
    },
    enabled: !!dsId,
    staleTime: 5_000,
  })

  useEffect(() => {
    return () => { if (timerRef.current) clearTimeout(timerRef.current) }
  }, [])

  const pagination = useMemo(() => ({
    current: page,
    pageSize,
    total: queryResult.data?.total ?? 0,
    showSizeChanger: true,
    showTotal: (t: number) => `共 ${t} 个`,
    onChange: (p: number, ps: number) => {
      if (ps !== pageSize) {
        setPageSize(ps)
        setPage(1)
      } else {
        setPage(p)
      }
    },
    onShowSizeChange: (_: unknown, ps: number) => {
      setPageSize(ps)
      setPage(1)
    },
  }), [page, pageSize, queryResult.data?.total])

  return {
    data: queryResult.data?.items ?? [],
    total: queryResult.data?.total ?? 0,
    isLoading: queryResult.isLoading,
    error: queryResult.error,
    refetch: queryResult.refetch,
    pagination,
    search, doSearch,
    phase, setPhase,
    namespace, setNamespace,
    nodeName, setNodeName,
    ready, setReady,
    labelSelector, setLabelSelector,
    page, setPage,
    /** Raw query result – access extra fields like availableStatuses */
    raw: queryResult,
  }
}
