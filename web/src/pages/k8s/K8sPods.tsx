/**
 * K8sPods – Pod / Deployment / DaemonSet 管理
 */
import { useState, useEffect, useRef, useMemo } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Table, Tag, Space, Input, Alert, Typography, Button,
  Tabs, Modal, Form, InputNumber, Popconfirm, message, Tooltip,
  Drawer, Upload, Radio, Select, Switch,
} from 'antd'
import {
  ReloadOutlined, EditOutlined, DeleteOutlined,
  ExpandAltOutlined, RedoOutlined, FileTextOutlined, CodeOutlined,
  UnorderedListOutlined, RocketOutlined, RobotOutlined,
  UploadOutlined, DownloadOutlined, RollbackOutlined, InfoCircleOutlined, LinkOutlined,
} from '@ant-design/icons'
import '@xterm/xterm/css/xterm.css'
import { useAuthStore } from '../../store/auth'
import { PageHeader } from '../../components/PageHeader'
import { SurfaceCard } from '../../components/SurfaceCard'
import { useTheme } from '../../hooks/useTheme'
import { http } from '../../api/request'
import { useClusters, useSelectedCluster, byCreation, fmtCreation, useNamespaces, useAutoRefresh, useK8sList } from './useCluster'
import { ClusterSelector } from './ClusterSelector'
import { YamlEditor } from './YamlEditor'
import { ConfigMapsTab } from './K8sConfigMaps'
import { VolumesTab } from './K8sVolumes'
import { HpasTab } from './K8sHpas'
import { EndpointsTab } from './K8sEndpoints'
import { K8sAIDrawer } from './K8sAIDrawer'

const { Text } = Typography

// ─── types ────────────────────────────────────────────────────────────────────

interface Container { name: string; image: string }
interface TerminatedState { reason?: string; exitCode?: number; signal?: number; message?: string; startedAt?: string; finishedAt?: string }
interface ContainerStatus {
  name: string; ready: boolean; restartCount: number
  state?: { waiting?: { reason?: string; message?: string }; terminated?: TerminatedState }
  lastState?: { terminated?: TerminatedState }
}
interface PodItem {
  metadata?: { name?: string; namespace?: string; creationTimestamp?: string; deletionTimestamp?: string }
  spec?: { nodeName?: string; containers?: Container[] }
  status?: { phase?: string; podIP?: string; containerStatuses?: ContainerStatus[]; reason?: string }
}
interface PodList { items?: PodItem[] }

interface DeployItem {
  metadata?: { name?: string; namespace?: string; creationTimestamp?: string }
  spec?: {
    replicas?: number
    selector?: { matchLabels?: Record<string, string> }
    template?: { spec?: { containers?: Container[] } }
  }
  status?: { replicas?: number; readyReplicas?: number; availableReplicas?: number }
}
interface DeployList { items?: DeployItem[] }

interface DsItem {
  metadata?: { name?: string; namespace?: string; creationTimestamp?: string }
  spec?: {
    selector?: { matchLabels?: Record<string, string> }
    template?: { spec?: { containers?: Container[] } }
  }
  status?: { desiredNumberScheduled?: number; numberReady?: number }
}
interface DsList { items?: DsItem[] }

interface HistoryEntry {
  revision: string
  name: string
  creationTimestamp: string
  replicas: number
  readyReplicas: number
  template: unknown
}

// ─── helpers ──────────────────────────────────────────────────────────────────

function podDerivedStatus(p: PodItem): string {
  if (p.metadata?.deletionTimestamp) return 'Terminating'
  const phase = p.status?.phase ?? 'Unknown'
  if (phase === 'Failed' && p.status?.reason === 'Evicted') return 'Evicted'
  for (const cs of p.status?.containerStatuses ?? []) {
    const r = cs.state?.waiting?.reason
    if (r) return r
  }
  for (const cs of p.status?.containerStatuses ?? []) {
    const r = cs.state?.terminated?.reason
    if (r && r !== 'Completed') return r
  }
  return phase
}

function podPhaseTag(status: string) {
  const map: Record<string, string> = {
    Running: 'success', Succeeded: 'default', Pending: 'warning',
    Failed: 'error', Unknown: 'default', Evicted: 'error',
    Terminating: 'orange', CrashLoopBackOff: 'red', OOMKilled: 'red',
    ImagePullBackOff: 'warning', ErrImagePull: 'warning', Error: 'error',
    ContainerCreating: 'processing', PodInitializing: 'processing',
  }
  return <Tag color={map[status] ?? 'default'} style={{ margin: 0 }}>{status ?? '—'}</Tag>
}

function firstImage(containers?: Container[]) {
  const img = containers?.[0]?.image ?? '—'
  return img.includes('/') ? img.split('/').pop() ?? img : img
}

// ─── useK8sGet – 延迟加载单个资源 ────────────────────────────────────────────

function useK8sGet(endpoint: string, params: Record<string, string>, enabled: boolean) {
  return useQuery<unknown>({
    queryKey: ['k8s-get', endpoint, params],
    queryFn: () => http.get<unknown>(endpoint, { params }),
    enabled,
    staleTime: 0,
  })
}

// ─── PodsTab ──────────────────────────────────────────────────────────────────

function PodsTab({ dsId, selectorFilter = '', nsFilter = '', nodeFilterProp = '' }: { dsId: string; selectorFilter?: string; nsFilter?: string; nodeFilterProp?: string }) {
  const { c } = useTheme()
  const qc = useQueryClient()
  const [ns, setNs] = useState(nsFilter)
  const [search, setSearch] = useState('')
  const [phaseFilter, setPhaseFilter] = useState('')
  const [readyFilter, setReadyFilter] = useState('')
  const [nodeFilter, setNodeFilter] = useState(nodeFilterProp)
  const [viewTarget, setViewTarget] = useState<{ ns: string; name: string } | null>(null)
  const [logModal, setLogModal] = useState<{ ns: string; name: string; container: string; restartCount?: number } | null>(null)
  const [eventModal, setEventModal] = useState<{ ns: string; name: string } | null>(null)
  const [execModal, setExecModal] = useState<{ ns: string; name: string; container: string; defaultCommand?: string } | null>(null)
  const [terminalTarget, setTerminalTarget] = useState<{ ns: string; name: string; container: string } | null>(null)
  const [describeTarget, setDescribeTarget] = useState<{ ns: string; name: string } | null>(null)

  const {
    data: pods,
    isLoading,
    error,
    refetch,
    pagination,
    search: searchValue,
    doSearch,
    phase,
    setPhase,
    nodeName: nodeNameFilter,
    setNodeName,
    ready,
    setReady,
  } = useK8sList<PodItem>('/k8s/pods', {
    dsId,
    namespace: ns || undefined,
    pageSize: 20,
    searchDelay: 300,
  })

  const { data: namespaces = [] } = useNamespaces(dsId)
  const autoRefresh = useAutoRefresh(refetch, 0)

  // 获取单个 pod 详情用于 YAML 查看
  const { data: podDetail, isFetching: podFetching } = useK8sGet(
    '/k8s/pod',
    { ds: dsId, namespace: viewTarget?.ns ?? '', name: viewTarget?.name ?? '' },
    !!viewTarget,
  )

  const deleteMut = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) =>
      http.delete(`/k8s/pod?ds=${dsId}&namespace=${ns}&name=${name}`),
    onSuccess: () => { message.success('Pod 已删除'); refetch() },
    onError: (e: Error) => message.error(e.message),
  })


  // Pod 资源用量（metrics-server，不可用时降级为空）
  interface PodMetric { metadata?: { name?: string; namespace?: string }; containers?: { name: string; usage: { cpu?: string; memory?: string } }[] }
  const { data: metricsData } = useQuery<{ items?: PodMetric[] }>({
    queryKey: ['k8s-pod-metrics', dsId, ns, searchValue, phase],
    queryFn: () => http.get('/k8s/pod/metrics', { params: { ds: dsId, ...(ns ? { namespace: ns } : {}) } }),
    enabled: !!dsId,
    staleTime: 15_000,
    refetchInterval: 30_000,
    retry: false,
  })
  const metricsMap = useMemo(() => {
    const m = new Map<string, { cpu: string; mem: string }>()
    for (const pm of metricsData?.items ?? []) {
      const key = `${pm.metadata?.namespace}/${pm.metadata?.name}`
      const cpuMilli = (pm.containers ?? []).reduce((sum, c) => {
        const v = c.usage?.cpu ?? '0'
        if (v.endsWith('n')) return sum + parseInt(v) / 1e6
        if (v.endsWith('u')) return sum + parseInt(v) / 1e3
        if (v.endsWith('m')) return sum + parseInt(v)
        return sum + parseFloat(v) * 1000
      }, 0)
      const memMi = (pm.containers ?? []).reduce((sum, c) => {
        const v = c.usage?.memory ?? '0'
        if (v.endsWith('Ki')) return sum + parseInt(v) / 1024
        if (v.endsWith('Mi')) return sum + parseInt(v)
        if (v.endsWith('Gi')) return sum + parseInt(v) * 1024
        return sum + parseInt(v) / (1024 * 1024)
      }, 0)
      m.set(key, { cpu: `${cpuMilli.toFixed(0)}m`, mem: memMi >= 1024 ? `${(memMi/1024).toFixed(1)}Gi` : `${memMi.toFixed(0)}Mi` })
    }
    return m
  }, [metricsData])

  const allStatuses = useMemo(() => {
    const set = new Set<string>();
    (pods ?? []).forEach(p => set.add(podDerivedStatus(p)))
    return Array.from(set).sort()
  }, [pods])

  const selectorPairs = selectorFilter ? selectorFilter.split(',').map(s => s.split('=')).filter(p => p.length === 2) : []

  const columns = [
    {
      title: 'Pod 名称',
      render: (_: unknown, p: PodItem) => (
        <Text style={{ fontFamily: 'monospace', fontSize: 12 }}>{p.metadata?.name}</Text>
      ),
    },
    { title: '命名空间', width: 130, render: (_: unknown, p: PodItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{p.metadata?.namespace}</span> },
    {
      title: 'READY', width: 75,
      render: (_: unknown, p: PodItem) => {
        const total = (p.spec?.containers ?? []).length
        const ready = (p.status?.containerStatuses ?? []).filter(cs => cs.ready).length
        const ok = ready === total && total > 0
        return <span style={{ fontSize: 12, fontFamily: 'monospace', color: ok ? c.success : c.warning }}>{ready}/{total}</span>
      },
    },
    { title: '状态', width: 130, render: (_: unknown, p: PodItem) => podPhaseTag(podDerivedStatus(p)) },
    {
      title: '重启次数', width: 90,
      render: (_: unknown, p: PodItem) => {
        const r = (p.status?.containerStatuses ?? []).reduce((a, c) => a + c.restartCount, 0)
        if (r === 0) return <span style={{ fontSize: 12, color: c.textSecondary }}>0</span>
        // 展示每个容器的上次退出信息
        const details = (p.status?.containerStatuses ?? [])
          .filter(cs => cs.lastState?.terminated)
          .map(cs => {
            const t = cs.lastState!.terminated!
            const parts = [`[${cs.name}]`, t.reason ?? 'Unknown']
            if (t.exitCode !== undefined) parts.push(`exitCode=${t.exitCode}`)
            if (t.signal !== undefined) parts.push(`signal=${t.signal}`)
            if (t.finishedAt) parts.push(`终止:${new Date(t.finishedAt).toLocaleString('zh-CN', { hour12: false })}`)
            return parts.join(' ')
          })
        const tip = details.length > 0 ? (
          <div style={{ maxWidth: 400 }}>
            {details.map((d, i) => <div key={i} style={{ fontFamily: 'monospace', fontSize: 11 }}>{d}</div>)}
          </div>
        ) : '暂无上次退出记录'
        return (
          <Space size={6}>
            <Tooltip title={tip} placement="right">
              <span style={{ fontSize: 12, color: c.warning, cursor: 'help', textDecoration: 'underline dotted' }}>{r}</span>
            </Tooltip>
            <Tooltip title="查看重启详情">
              <Button size="small" type="text" icon={<InfoCircleOutlined />} style={{ color: c.textSecondary, padding: 0, height: 16, width: 16 }}
                onClick={() => setDescribeTarget({ ns: p.metadata?.namespace ?? '', name: p.metadata?.name ?? '' })} />
            </Tooltip>
          </Space>
        )
      },
    },
    { title: 'Pod IP', width: 130, render: (_: unknown, p: PodItem) => <span style={{ fontSize: 12, fontFamily: 'monospace', color: c.textSecondary }}>{p.status?.podIP ?? '—'}</span> },
    {
      title: 'CPU / 内存', width: 120,
      render: (_: unknown, p: PodItem) => {
        const key = `${p.metadata?.namespace}/${p.metadata?.name}`
        const m = metricsMap.get(key)
        if (!m) return <span style={{ fontSize: 11, color: c.textHint }}>—</span>
        return (
          <span style={{ fontSize: 11, fontFamily: 'monospace', color: c.textSecondary }}>
            {m.cpu} / {m.mem}
          </span>
        )
      },
    },
    { title: '节点', width: 150, render: (_: unknown, p: PodItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{p.spec?.nodeName ?? '—'}</span> },
    { title: '运行时长', width: 90, render: (_: unknown, p: PodItem) => <span style={{ fontSize: 12, color: c.textHint }}>{fmtCreation(p.metadata?.creationTimestamp).age}</span> },
    {
      title: '操作', width: 200, fixed: 'right' as const,
      render: (_: unknown, p: PodItem) => {
        const firstContainer = p.spec?.containers?.[0]?.name ?? ''
        const podNs = p.metadata?.namespace ?? ''
        const podName = p.metadata?.name ?? ''
        return (
          <Space size={4}>
            <Tooltip title="进入容器">
              <Button size="small" type="text" icon={<CodeOutlined />}
                onClick={() => setTerminalTarget({ ns: podNs, name: podName, container: firstContainer })} />
            </Tooltip>
            <Tooltip title="日志">
              <Button size="small" type="text" icon={<FileTextOutlined />}
                onClick={() => setLogModal({ ns: podNs, name: podName, container: firstContainer, restartCount: (p.status?.containerStatuses ?? []).reduce((a, c) => a + c.restartCount, 0) })} />
            </Tooltip>
            <Tooltip title="事件">
              <Button size="small" type="text" icon={<UnorderedListOutlined />}
                onClick={() => setEventModal({ ns: podNs, name: podName })} />
            </Tooltip>
            <Tooltip title="进程">
              <Button size="small" type="text" icon={<RocketOutlined />}
                onClick={() => setExecModal({ ns: podNs, name: podName, container: firstContainer, defaultCommand: 'ps aux' })} />
            </Tooltip>
            <Popconfirm
              title={`确认删除 Pod "${podName}"？`}
              okText="删除" cancelText="取消" okButtonProps={{ danger: true }}
              onConfirm={() => deleteMut.mutate({ ns: podNs, name: podName })}
            >
              <Tooltip title="删除">
                <Button size="small" type="text" danger icon={<DeleteOutlined />} />
              </Tooltip>
            </Popconfirm>
          </Space>
        )
      },
    },
  ]

  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <Select
          style={{ width: 240 }}
          placeholder="命名空间（全部）"
          allowClear
          showSearch
          value={ns || undefined}
          onChange={v => setNs(v ?? '')}
          options={namespaces.map(n => ({ label: n, value: n }))}
        />
        <Input.Search placeholder="搜索 Pod 名称" allowClear style={{ width: 200 }}
          onSearch={doSearch} onChange={e => !e.target.value && doSearch('')} />
        <Select style={{ width: 160 }} placeholder="状态" allowClear value={phase || undefined}
          onChange={v => { setPhase(v ?? ''); setPage(1) }}
          options={allStatuses.map(s => ({ label: s, value: s }))} />
        <Select style={{ width: 110 }} placeholder="READY" allowClear value={ready || undefined}
          onChange={v => setReady(v ?? '')}
          options={[
            { label: '全部就绪', value: 'ready' },
            { label: '未就绪', value: 'notready' },
          ]} />
        <Input.Search placeholder="节点名" allowClear style={{ width: 200 }}
          onSearch={setNodeName} onChange={e => !e.target.value && setNodeName('')} />
        <Button icon={<ReloadOutlined />} onClick={() => refetch()} loading={isLoading}>刷新</Button>
        <Space size={4}>
          <Switch
            size="small"
            checked={autoRefresh.enabled}
            onChange={autoRefresh.setEnabled}
          />
          <span style={{ fontSize: 12 }}>自动刷新</span>
          {autoRefresh.enabled && (
            <InputNumber
              size="small"
              min={3} max={3600}
              value={autoRefresh.interval}
              onChange={v => autoRefresh.setInterval(v ?? 30)}
              addonAfter="s"
              style={{ width: 90 }}
            />
          )}
        </Space>
      </Space>
      {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 8 }} />}
      <Table dataSource={pods} columns={columns} rowKey={p => `${p.metadata?.namespace}/${p.metadata?.name}`}
        loading={isLoading} size="small" scroll={{ x: 'max-content' }}
        pagination={pagination} />

      <YamlEditor
        title={`Pod: ${viewTarget?.name ?? ''}`}
        value={podDetail}
        open={!!viewTarget}
        onClose={() => setViewTarget(null)}
        loading={podFetching}
      />
      <PodLogModal dsId={dsId} open={!!logModal} onClose={() => setLogModal(null)} target={logModal} />
      <PodEventModal dsId={dsId} open={!!eventModal} onClose={() => setEventModal(null)} target={eventModal} />
      <PodExecModal dsId={dsId} open={!!execModal} onClose={() => setExecModal(null)} target={execModal} defaultCommand={execModal?.defaultCommand} />
      <PodTerminalDrawer dsId={dsId} open={!!terminalTarget} onClose={() => setTerminalTarget(null)} target={terminalTarget} />
      <PodDescribeDrawer dsId={dsId} open={!!describeTarget} onClose={() => setDescribeTarget(null)} target={describeTarget} />
    </>
  )
}

// ─── DeploymentsTab ───────────────────────────────────────────────────────────

function DeploymentsTab({ dsId }: { dsId: string }) {
  const { c } = useTheme()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [ns, setNs] = useState('')
  const [search, setSearch] = useState('')
  const [scaleTarget, setScaleTarget] = useState<DeployItem | null>(null)
  const [scaleForm] = Form.useForm()
  const [editTarget, setEditTarget] = useState<{ item: DeployItem; data: unknown } | null>(null)
  const [eventTarget, setEventTarget] = useState<{ ns: string; name: string } | null>(null)
  // 回滚功能 state
  const [rollbackTarget, setRollbackTarget] = useState<{ ns: string; name: string } | null>(null)
  const [selectedRevision, setSelectedRevision] = useState<string | null>(null)

  const {
    data: deploys,
    isLoading,
    error,
    refetch,
    pagination,
    doSearch,
  } = useK8sList<DeployItem>('/k8s/deployments', {
    dsId,
    namespace: ns || undefined,
    pageSize: 20,
    searchDelay: 300,
  })

  const { data: namespaces = [] } = useNamespaces(dsId)
  const autoRefresh = useAutoRefresh(refetch, 0)

  // 获取单个 deploy JSON
  const [getTarget, setGetTarget] = useState<{ ns: string; name: string } | null>(null)
  const { data: deployDetail, isFetching: deployFetching } = useK8sGet(
    '/k8s/deployment',
    { ds: dsId, namespace: getTarget?.ns ?? '', name: getTarget?.name ?? '' },
    !!getTarget,
  )
  // 当 detail 加载完毕，打开编辑器
  useState(() => {
    if (deployDetail && getTarget) {
      const item = (data?.items ?? []).find(d =>
        d.metadata?.name === getTarget.name && d.metadata?.namespace === getTarget.ns
      )
      if (item) setEditTarget({ item, data: deployDetail })
    }
  })

  const scaleMut = useMutation({
    mutationFn: ({ ns, name, replicas }: { ns: string; name: string; replicas: number }) =>
      http.post(`/k8s/deployment/scale?ds=${dsId}&namespace=${ns}&name=${name}`, { replicas }),
    onSuccess: () => { message.success('扩缩容成功'); setScaleTarget(null); refetch() },
    onError: (e: Error) => message.error(e.message),
  })

  const restartMut = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) =>
      http.post(`/k8s/deployment/restart?ds=${dsId}&namespace=${ns}&name=${name}`),
    onSuccess: () => { message.success('滚动重启已触发'); refetch() },
    onError: (e: Error) => message.error(e.message),
  })

  // 回滚历史版本查询
  const { data: historyData = [], isFetching: historyLoading } = useQuery<HistoryEntry[]>({
    queryKey: ['k8s-deploy-history', dsId, rollbackTarget?.ns, rollbackTarget?.name],
    queryFn: () => http.get<HistoryEntry[]>('/k8s/deployment/history', {
      params: { ds: dsId, namespace: rollbackTarget!.ns, name: rollbackTarget!.name },
    }),
    enabled: !!rollbackTarget,
  })

  const rollbackMut = useMutation({
    mutationFn: ({ ns, name, revision }: { ns: string; name: string; revision: string }) =>
      http.post(`/k8s/deployment/rollback?ds=${dsId}&namespace=${ns}&name=${name}&revision=${revision}`),
    onSuccess: () => {
      message.success('回滚成功')
      setRollbackTarget(null)
      setSelectedRevision(null)
      refetch()
    },
    onError: (e: Error) => message.error(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) =>
      http.delete(`/k8s/deployment?ds=${dsId}&namespace=${ns}&name=${name}`),
    onSuccess: () => { message.success('Deployment 已删除'); refetch() },
    onError: (e: Error) => message.error(e.message),
  })

  const updateMut = useMutation({
    mutationFn: ({ ns, name, body }: { ns: string; name: string; body: string }) =>
      http.put(`/k8s/deployment?ds=${dsId}&namespace=${ns}&name=${name}`, JSON.parse(body)),
    onSuccess: () => { message.success('Deployment 已更新'); setEditTarget(null); setGetTarget(null); refetch() },
    onError: (e: Error) => message.error(e.message),
  })

  const columns = [
    { title: 'Deployment 名称', render: (_: unknown, d: DeployItem) => <Text style={{ fontFamily: 'monospace', fontSize: 12 }}>{d.metadata?.name}</Text> },
    { title: '命名空间', width: 130, render: (_: unknown, d: DeployItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{d.metadata?.namespace}</span> },
    {
      title: '副本', width: 90,
      render: (_: unknown, d: DeployItem) => {
        const ready = d.status?.readyReplicas ?? 0
        const desired = d.spec?.replicas ?? 0
        const ok = ready === desired
        return <span style={{ fontSize: 12, color: ok ? c.success : c.warning }}>{ready}/{desired}</span>
      },
    },
    { title: '镜像', onCell: () => ({ style: { wordBreak: 'break-all' as const, whiteSpace: 'normal' as const } }), render: (_: unknown, d: DeployItem) => <span style={{ fontSize: 11, color: c.textSecondary, fontFamily: 'monospace' }}>{firstImage(d.spec?.template?.spec?.containers)}</span> },
    { title: '运行时长', width: 90, render: (_: unknown, d: DeployItem) => <span style={{ fontSize: 12, color: c.textHint }}>{fmtCreation(d.metadata?.creationTimestamp).age}</span> },
    {
      title: '操作', width: 230, fixed: 'right' as const,
      render: (_: unknown, d: DeployItem) => (
        <Space size={4}>
          <Tooltip title="查看 Pod">
            <Button size="small" type="text" icon={<LinkOutlined />}
              onClick={() => navigate(`/k8s/resources?ds=${dsId}&tab=pods&selector=${encodeURIComponent(Object.entries(d.spec?.selector?.matchLabels ?? {}).map(([k,v]) => `${k}=${v}`).join(','))}&ns=${d.metadata?.namespace ?? ''}`)} />
          </Tooltip>
          <Tooltip title="扩缩容">
            <Button size="small" type="text" icon={<ExpandAltOutlined />}
              onClick={() => { setScaleTarget(d); scaleForm.setFieldsValue({ replicas: d.spec?.replicas ?? 1 }) }} />
          </Tooltip>
          <Tooltip title="事件">
            <Button size="small" type="text" icon={<UnorderedListOutlined />}
              onClick={() => setEventTarget({ ns: d.metadata?.namespace ?? '', name: d.metadata?.name ?? '' })} />
          </Tooltip>
          <Tooltip title="编辑 JSON">
            <Button size="small" type="text" icon={<EditOutlined />}
              loading={deployFetching && getTarget?.name === d.metadata?.name}
              onClick={() => setGetTarget({ ns: d.metadata?.namespace ?? '', name: d.metadata?.name ?? '' })} />
          </Tooltip>
          <Tooltip title="滚动重启">
            <Popconfirm title="确认触发滚动重启？" okText="重启" cancelText="取消"
              onConfirm={() => restartMut.mutate({ ns: d.metadata?.namespace ?? '', name: d.metadata?.name ?? '' })}>
              <Button size="small" type="text" icon={<RedoOutlined />} />
            </Popconfirm>
          </Tooltip>
          <Tooltip title="回滚">
            <Button size="small" type="text" icon={<RollbackOutlined />}
              onClick={() => { setRollbackTarget({ ns: d.metadata?.namespace ?? '', name: d.metadata?.name ?? '' }); setSelectedRevision(null) }} />
          </Tooltip>
          <Popconfirm title={`确认删除 "${d.metadata?.name}"？`} okText="删除" cancelText="取消" okButtonProps={{ danger: true }}
            onConfirm={() => deleteMut.mutate({ ns: d.metadata?.namespace ?? '', name: d.metadata?.name ?? '' })}>
            <Tooltip title="删除">
              <Button size="small" type="text" danger icon={<DeleteOutlined />} />
            </Tooltip>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <Select
          style={{ width: 240 }}
          placeholder="命名空间（全部）"
          allowClear
          showSearch
          value={ns || undefined}
          onChange={v => setNs(v ?? '')}
          options={namespaces.map(n => ({ label: n, value: n }))}
        />
        <Input.Search placeholder="搜索 Deployment 名称" allowClear style={{ width: 240 }}
          onSearch={doSearch} onChange={e => !e.target.value && doSearch('')} />
        <Button icon={<ReloadOutlined />} onClick={() => refetch()} loading={isLoading}>刷新</Button>
        <Space size={4}>
          <Switch size="small" checked={autoRefresh.enabled} onChange={autoRefresh.setEnabled} />
          <span style={{ fontSize: 12 }}>自动刷新</span>
          {autoRefresh.enabled && (
            <InputNumber size="small" min={3} max={3600} value={autoRefresh.interval}
              onChange={v => autoRefresh.setInterval(v ?? 30)} addonAfter="s" style={{ width: 90 }} />
          )}
        </Space>
      </Space>
      {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 8 }} />}
      <Table dataSource={deploys} columns={columns}
        rowKey={d => `${d.metadata?.namespace}/${d.metadata?.name}`}
        loading={isLoading} size="small" scroll={{ x: 'max-content' }}
        pagination={pagination} />

      {/* 扩缩容 Modal */}
      <Modal
        title={`扩缩容 — ${scaleTarget?.metadata?.name}`}
        open={!!scaleTarget}
        onCancel={() => setScaleTarget(null)}
        onOk={() => {
          scaleForm.validateFields().then(v => {
            scaleMut.mutate({
              ns: scaleTarget?.metadata?.namespace ?? '',
              name: scaleTarget?.metadata?.name ?? '',
              replicas: v.replicas,
            })
          })
        }}
        confirmLoading={scaleMut.isPending}
        okText="确认"
      >
        <Form form={scaleForm} layout="vertical">
          <Form.Item name="replicas" label="副本数" rules={[{ required: true, min: 0 }]}>
            <InputNumber min={0} max={100} style={{ width: '100%' }} />
          </Form.Item>
        </Form>
      </Modal>

      {/* JSON 编辑器 */}
      <YamlEditor
        title={`编辑 Deployment: ${editTarget?.item.metadata?.name ?? getTarget?.name ?? ''}`}
        value={editTarget?.data ?? deployDetail}
        open={!!(editTarget || (deployDetail && getTarget))}
        onClose={() => { setEditTarget(null); setGetTarget(null) }}
        loading={updateMut.isPending}
        onSave={json => updateMut.mutate({
          ns: editTarget?.item.metadata?.namespace ?? getTarget?.ns ?? '',
          name: editTarget?.item.metadata?.name ?? getTarget?.name ?? '',
          body: json,
        })}
      />

      {/* 事件弹窗 */}
      <ResourceEventModal
        dsId={dsId}
        open={!!eventTarget}
        onClose={() => setEventTarget(null)}
        target={eventTarget}
        kind="Deployment"
      />

      {/* 回滚弹窗 */}
      <Modal
        title={`回滚版本 — ${rollbackTarget?.name}`}
        open={!!rollbackTarget}
        onCancel={() => { setRollbackTarget(null); setSelectedRevision(null) }}
        onOk={() => {
          if (!selectedRevision || !rollbackTarget) return
          rollbackMut.mutate({ ns: rollbackTarget.ns, name: rollbackTarget.name, revision: selectedRevision })
        }}
        confirmLoading={rollbackMut.isPending}
        okText="确认回滚"
        okButtonProps={{ disabled: !selectedRevision }}
        width={680}
      >
        <p style={{ marginBottom: 12, color: '#888', fontSize: 12 }}>选择要回滚到的历史版本：</p>
        <Table<{ revision: string; name: string; creationTimestamp: string; replicas: number; readyReplicas: number; template: unknown }>
          loading={historyLoading}
          dataSource={historyData}
          rowKey="revision"
          size="small"
          pagination={false}
          rowSelection={{
            type: 'radio',
            selectedRowKeys: selectedRevision ? [selectedRevision] : [],
            onChange: keys => setSelectedRevision(keys[0] as string),
          }}
          columns={[
            { title: '版本号', dataIndex: 'revision', width: 70,
              render: (v: string) => <strong>#{v}</strong> },
            { title: '镜像', dataIndex: 'template',
              onCell: () => ({ style: { wordBreak: 'break-all' as const, whiteSpace: 'normal' as const } }),
              render: (tpl: { spec?: { containers?: { image?: string }[] } }) => {
                const img = tpl?.spec?.containers?.[0]?.image ?? '—'
                return <span style={{ fontFamily: 'monospace', fontSize: 11 }}>{img}</span>
              } },
            { title: '副本数', dataIndex: 'replicas', width: 70,
              render: (v: number, r: { readyReplicas: number }) => `${r.readyReplicas}/${v}` },
            { title: '创建时间', dataIndex: 'creationTimestamp', width: 150,
              render: (v: string) => fmtCreation(v).date },
          ]}
        />
      </Modal>
    </>
  )
}

// ─── DaemonSetsTab ────────────────────────────────────────────────────────────

function DaemonSetsTab({ dsId }: { dsId: string }) {
  const { c } = useTheme()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [ns, setNs] = useState('')
  const [search, setSearch] = useState('')
  const [getTarget, setGetTarget] = useState<{ ns: string; name: string } | null>(null)
  const [eventTarget, setEventTarget] = useState<{ ns: string; name: string } | null>(null)
  const [dsRollbackTarget, setDsRollbackTarget] = useState<{ ns: string; name: string } | null>(null)
  const [dsSelectedRevision, setDsSelectedRevision] = useState<string | null>(null)

  const { data, isLoading, error, refetch } = useQuery<DsList>({
    queryKey: ['k8s-daemonsets', dsId, ns],
    queryFn: () => http.get<DsList>('/k8s/daemonsets', {
      params: { ds: dsId, ...(ns ? { namespace: ns } : {}) },
    }),
    enabled: !!dsId,
    staleTime: 10_000,
  })

  const { data: namespaces = [] } = useNamespaces(dsId)
  const autoRefresh = useAutoRefresh(() => refetch(), 0)

  const { data: dsDetail, isFetching: dsFetching } = useK8sGet(
    '/k8s/daemonset',
    { ds: dsId, namespace: getTarget?.ns ?? '', name: getTarget?.name ?? '' },
    !!getTarget,
  )

  const restartMut = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) =>
      http.post(`/k8s/daemonset/restart?ds=${dsId}&namespace=${ns}&name=${name}`),
    onSuccess: () => { message.success('滚动重启已触发'); refetch() },
    onError: (e: Error) => message.error(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) =>
      http.delete(`/k8s/daemonset?ds=${dsId}&namespace=${ns}&name=${name}`),
    onSuccess: () => { message.success('DaemonSet 已删除'); refetch() },
    onError: (e: Error) => message.error(e.message),
  })

  const updateMut = useMutation({
    mutationFn: ({ ns, name, body }: { ns: string; name: string; body: string }) =>
      http.put(`/k8s/daemonset?ds=${dsId}&namespace=${ns}&name=${name}`, JSON.parse(body)),
    onSuccess: () => { message.success('DaemonSet 已更新'); setGetTarget(null); refetch() },
    onError: (e: Error) => message.error(e.message),
  })

  const { data: dsHistoryData = [], isFetching: dsHistoryLoading } = useQuery<HistoryEntry[]>({
    queryKey: ['k8s-ds-history', dsId, dsRollbackTarget?.ns, dsRollbackTarget?.name],
    queryFn: () => http.get<HistoryEntry[]>('/k8s/daemonset/history', {
      params: { ds: dsId, namespace: dsRollbackTarget!.ns, name: dsRollbackTarget!.name },
    }),
    enabled: !!dsRollbackTarget,
  })

  const dsRollbackMut = useMutation({
    mutationFn: ({ ns, name, revision }: { ns: string; name: string; revision: string }) =>
      http.post(`/k8s/daemonset/rollback?ds=${dsId}&namespace=${ns}&name=${name}&revision=${revision}`),
    onSuccess: () => {
      message.success('回滚成功')
      setDsRollbackTarget(null)
      setDsSelectedRevision(null)
      refetch()
    },
    onError: (e: Error) => message.error(e.message),
  })

  const items = (data?.items ?? []).filter(d =>
    !search || (d.metadata?.name ?? '').toLowerCase().includes(search.toLowerCase())
  ).sort(byCreation)

  const columns = [
    { title: 'DaemonSet 名称', render: (_: unknown, d: DsItem) => <Text style={{ fontFamily: 'monospace', fontSize: 12 }}>{d.metadata?.name}</Text> },
    { title: '命名空间', width: 130, render: (_: unknown, d: DsItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{d.metadata?.namespace}</span> },
    {
      title: '节点', width: 90,
      render: (_: unknown, d: DsItem) => {
        const ready = d.status?.numberReady ?? 0
        const desired = d.status?.desiredNumberScheduled ?? 0
        const ok = ready === desired
        return <span style={{ fontSize: 12, color: ok ? c.success : c.warning }}>{ready}/{desired}</span>
      },
    },
    { title: '镜像', onCell: () => ({ style: { wordBreak: 'break-all' as const, whiteSpace: 'normal' as const } }), render: (_: unknown, d: DsItem) => <span style={{ fontSize: 11, color: c.textSecondary, fontFamily: 'monospace' }}>{firstImage(d.spec?.template?.spec?.containers)}</span> },
    { title: '运行时长', width: 90, render: (_: unknown, d: DsItem) => <span style={{ fontSize: 12, color: c.textHint }}>{fmtCreation(d.metadata?.creationTimestamp).age}</span> },
    {
      title: '操作', width: 220, fixed: 'right' as const,
      render: (_: unknown, d: DsItem) => (
        <Space size={4}>
          <Tooltip title="查看 Pod">
            <Button size="small" type="text" icon={<LinkOutlined />}
              onClick={() => navigate(`/k8s/resources?ds=${dsId}&tab=pods&selector=${encodeURIComponent(Object.entries(d.spec?.selector?.matchLabels ?? {}).map(([k,v]) => `${k}=${v}`).join(','))}&ns=${d.metadata?.namespace ?? ''}`)} />
          </Tooltip>
          <Tooltip title="事件">
            <Button size="small" type="text" icon={<UnorderedListOutlined />}
              onClick={() => setEventTarget({ ns: d.metadata?.namespace ?? '', name: d.metadata?.name ?? '' })} />
          </Tooltip>
          <Tooltip title="编辑 JSON">
            <Button size="small" type="text" icon={<EditOutlined />}
              loading={dsFetching && getTarget?.name === d.metadata?.name}
              onClick={() => setGetTarget({ ns: d.metadata?.namespace ?? '', name: d.metadata?.name ?? '' })} />
          </Tooltip>
          <Tooltip title="滚动重启">
            <Popconfirm title="确认触发滚动重启？" okText="重启" cancelText="取消"
              onConfirm={() => restartMut.mutate({ ns: d.metadata?.namespace ?? '', name: d.metadata?.name ?? '' })}>
              <Button size="small" type="text" icon={<RedoOutlined />} />
            </Popconfirm>
          </Tooltip>
          <Tooltip title="回滚">
            <Button size="small" type="text" icon={<RollbackOutlined />}
              onClick={() => { setDsRollbackTarget({ ns: d.metadata?.namespace ?? '', name: d.metadata?.name ?? '' }); setDsSelectedRevision(null) }} />
          </Tooltip>
          <Popconfirm title={`确认删除 "${d.metadata?.name}"？`} okText="删除" cancelText="取消" okButtonProps={{ danger: true }}
            onConfirm={() => deleteMut.mutate({ ns: d.metadata?.namespace ?? '', name: d.metadata?.name ?? '' })}>
            <Tooltip title="删除">
              <Button size="small" type="text" danger icon={<DeleteOutlined />} />
            </Tooltip>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <Select
          style={{ width: 240 }}
          placeholder="命名空间（全部）"
          allowClear
          showSearch
          value={ns || undefined}
          onChange={v => setNs(v ?? '')}
          options={namespaces.map(n => ({ label: n, value: n }))}
        />
        <Input.Search placeholder="搜索 DaemonSet 名称" allowClear style={{ width: 240 }}
          onSearch={setSearch} onChange={e => !e.target.value && setSearch('')} />
        <Button icon={<ReloadOutlined />} onClick={() => refetch()} loading={isLoading}>刷新</Button>
        <Space size={4}>
          <Switch size="small" checked={autoRefresh.enabled} onChange={autoRefresh.setEnabled} />
          <span style={{ fontSize: 12 }}>自动刷新</span>
          {autoRefresh.enabled && (
            <InputNumber size="small" min={3} max={3600} value={autoRefresh.interval}
              onChange={v => autoRefresh.setInterval(v ?? 30)} addonAfter="s" style={{ width: 90 }} />
          )}
        </Space>
      </Space>
      {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 8 }} />}
      <Table dataSource={items} columns={columns}
        rowKey={d => `${d.metadata?.namespace}/${d.metadata?.name}`}
        loading={isLoading} size="small" scroll={{ x: 'max-content' }}
        pagination={k8sPagination} />

      <YamlEditor
        title={`编辑 DaemonSet: ${getTarget?.name ?? ''}`}
        value={dsDetail}
        open={!!getTarget && !!dsDetail}
        onClose={() => setGetTarget(null)}
        loading={updateMut.isPending}
        onSave={json => updateMut.mutate({
          ns: getTarget?.ns ?? '',
          name: getTarget?.name ?? '',
          body: json,
        })}
      />

      {/* 事件弹窗 */}
      <ResourceEventModal
        dsId={dsId}
        open={!!eventTarget}
        onClose={() => setEventTarget(null)}
        target={eventTarget}
        kind="DaemonSet"
      />

      {/* 回滚弹窗 */}
      <Modal
        title={`回滚版本 — ${dsRollbackTarget?.name}`}
        open={!!dsRollbackTarget}
        okText="确认回滚"
        cancelText="Cancel"
        okButtonProps={{ disabled: !dsSelectedRevision }}
        width={680}
        confirmLoading={dsRollbackMut.isPending}
        onCancel={() => { setDsRollbackTarget(null); setDsSelectedRevision(null) }}
        onOk={() => dsRollbackMut.mutate({ ns: dsRollbackTarget!.ns, name: dsRollbackTarget!.name, revision: dsSelectedRevision! })}
      >
        <p style={{ marginBottom: 8, fontSize: 12, color: '#888' }}>选择要回滚到的历史版本：</p>
        <Table<HistoryEntry>
          dataSource={dsHistoryData}
          rowKey="revision"
          size="small"
          loading={dsHistoryLoading}
          pagination={false}
          rowSelection={{
            type: 'radio',
            selectedRowKeys: dsSelectedRevision ? [dsSelectedRevision] : [],
            onChange: keys => setDsSelectedRevision(keys[0] as string),
          }}
          columns={[
            { title: '版本号', dataIndex: 'revision', width: 70,
              render: (v: string) => <strong>#{v}</strong> },
            { title: '镜像', dataIndex: 'template',
              onCell: () => ({ style: { wordBreak: 'break-all' as const, whiteSpace: 'normal' as const } }),
              render: (tpl: { spec?: { containers?: { image?: string }[] } }) => {
                const img = tpl?.spec?.containers?.[0]?.image ?? '—'
                return <span style={{ fontFamily: 'monospace', fontSize: 11 }}>{img}</span>
              } },
            { title: '创建时间', dataIndex: 'creationTimestamp', width: 150,
              render: (v: string) => fmtCreation(v).date },
          ]}
        />
      </Modal>
    </>
  )
}

// ─── PodDescribeDrawer ──────────────────────────────────────────────────────────────────

function PodDescribeDrawer({
  dsId, open, onClose, target,
}: { dsId: string; open: boolean; onClose: () => void; target: { ns: string; name: string } | null }) {
  const { c } = useTheme()
  const [aiOpen, setAiOpen] = useState(false)
  const [logModal, setLogModal] = useState<{ ns: string; name: string; container: string; restartCount?: number } | null>(null)
  const { data: pod, isLoading } = useQuery<any>({
    queryKey: ['k8s-pod-describe', dsId, target?.ns, target?.name],
    queryFn: () => http.get<any>(`/k8s/pod/describe?ds=${dsId}&namespace=${target?.ns}&name=${target?.name}`),
    enabled: open && !!target,
    staleTime: 0,
  })

  const containerStatuses: any[] = pod?.status?.containerStatuses ?? []
  const initStatuses: any[] = pod?.status?.initContainerStatuses ?? []
  const allStatuses = [...initStatuses, ...containerStatuses]
  const conditions: any[] = pod?.status?.conditions ?? []
  const containers: any[] = pod?.spec?.containers ?? []
  const initContainers: any[] = pod?.spec?.initContainers ?? []

  const fmtTime = (t?: string) => t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '—'
  const exitColor = (code: number) => code === 0 ? c.success : c.danger

  // 生成 AI 分析用的文本描述
  const describeText = () => {
    if (!pod) return ''
    const lines: string[] = []
    lines.push(`Pod: ${pod.metadata?.name} (ns: ${pod.metadata?.namespace})`)
    lines.push(`节点: ${pod.spec?.nodeName ?? '—'}  IP: ${pod.status?.podIP ?? '—'}  阶段: ${pod.status?.phase ?? '—'}`)
    lines.push('')
    lines.push('=== 容器状态 ===')
    for (const cs of allStatuses) {
      lines.push(`  [${cs.name}] 重启:${cs.restartCount}`)
      const cur = cs.state
      if (cur?.running) lines.push(`    当前: Running 自 ${fmtTime(cur.running.startedAt)}`)
      if (cur?.waiting) lines.push(`    当前: Waiting ${cur.waiting.reason ?? ''} ${cur.waiting.message ?? ''}`)
      if (cur?.terminated) lines.push(`    当前: Terminated reason=${cur.terminated.reason} exitCode=${cur.terminated.exitCode}`)
      const last = cs.lastState?.terminated
      if (last) {
        lines.push(`    上次: reason=${last.reason} exitCode=${last.exitCode} signal=${last.signal ?? '—'}`)
        lines.push(`          开始:${fmtTime(last.startedAt)} 结束:${fmtTime(last.finishedAt)}`)
        if (last.message) lines.push(`          消息: ${last.message}`)
      }
    }
    lines.push('')
    lines.push('=== 容器资源 ===')
    for (const c of [...containers, ...initContainers]) {
      const req = c.resources?.requests ?? {}
      const lim = c.resources?.limits ?? {}
      lines.push(`  [${c.name}] image=${c.image}`)
      lines.push(`    requests: cpu=${req.cpu ?? '—'} mem=${req.memory ?? '—'}`)
      lines.push(`    limits:   cpu=${lim.cpu ?? '—'} mem=${lim.memory ?? '—'}`)
    }
    lines.push('')
    lines.push('=== 健康检查 ===')
    for (const cond of conditions) {
      lines.push(`  ${cond.type}: ${cond.status}${cond.reason ? ` (${cond.reason})` : ''}${cond.message ? ' - ' + cond.message : ''}`)
    }
    return lines.join('\n')
  }

  const secBg = { background: c.bgElevated, borderRadius: 6, padding: '10px 14px', marginBottom: 12 }
  const labelStyle = { fontSize: 11, color: c.textHint, marginRight: 6 }
  const valStyle = { fontSize: 12, fontFamily: 'monospace', color: c.textBody }

  return (
    <>
      <Drawer
        title={`Pod 详情: ${target?.name ?? ''}`}
        open={open}
        onClose={onClose}
        width={700}
        extra={
          <Button icon={<RobotOutlined />} onClick={() => setAiOpen(true)} disabled={!pod}
            style={{ color: '#722ed1', borderColor: '#722ed1' }}>AI 分析</Button>
        }
      >
        {isLoading && <div style={{ padding: 40, textAlign: 'center', color: c.textSecondary }}>加载中...</div>}
        {pod && (
          <div>
            {/* 基本信息 */}
            <div style={secBg}>
              <div style={{ fontSize: 13, fontWeight: 600, marginBottom: 8, color: c.textBody }}>基本信息</div>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '4px 12px', fontSize: 12 }}>
                <span><span style={labelStyle}>命名空间</span><span style={valStyle}>{pod.metadata?.namespace}</span></span>
                <span><span style={labelStyle}>阶段</span><span style={valStyle}>{pod.status?.phase}</span></span>
                <span><span style={labelStyle}>节点</span><span style={valStyle}>{pod.spec?.nodeName ?? '—'}</span></span>
                <span><span style={labelStyle}>Pod IP</span><span style={valStyle}>{pod.status?.podIP ?? '—'}</span></span>
                <span><span style={labelStyle}>创建时间</span><span style={valStyle}>{fmtTime(pod.metadata?.creationTimestamp)}</span></span>
                <span><span style={labelStyle}>QoS</span><span style={valStyle}>{pod.status?.qosClass ?? '—'}</span></span>
              </div>
            </div>

            {/* 容器状态 */}
            <div style={secBg}>
              <div style={{ fontSize: 13, fontWeight: 600, marginBottom: 8, color: c.textBody }}>容器状态</div>
              {allStatuses.map((cs: any) => {
                const last = cs.lastState?.terminated
                const cur = cs.state
                return (
                  <div key={cs.name} style={{ marginBottom: 14, paddingBottom: 10, borderBottom: `1px solid ${c.border}` }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
                      <span style={{ fontFamily: 'monospace', fontSize: 13, color: c.textBody, fontWeight: 600 }}>{cs.name}</span>
                      <Space size={4}>
                        <Tag color={cs.ready ? 'success' : 'error'} style={{ margin: 0 }}>{cs.ready ? 'Ready' : 'Not Ready'}</Tag>
                        {cs.restartCount > 0 && <Tag color="orange" style={{ margin: 0 }}>重启 {cs.restartCount} 次</Tag>}
                        <Button size="small" type="link" style={{ padding: 0, height: 'auto', fontSize: 11 }}
                          onClick={() => setLogModal({ ns: target!.ns, name: target!.name, container: cs.name, restartCount: cs.restartCount })}>查看日志</Button>
                      </Space>
                    </div>
                    {/* 当前状态 */}
                    {cur?.running && <div style={{ fontSize: 12 }}><span style={labelStyle}>当前</span><Tag color="success" style={{ margin: 0 }}>Running</Tag> <span style={labelStyle}>自</span><span style={valStyle}>{fmtTime(cur.running.startedAt)}</span></div>}
                    {cur?.waiting && (
                      <div style={{ fontSize: 12 }}>
                        <span style={labelStyle}>当前</span><Tag color="warning" style={{ margin: 0 }}>Waiting</Tag>
                        <span style={{ marginLeft: 6, color: c.warning, fontFamily: 'monospace', fontSize: 11 }}>{cur.waiting.reason}{cur.waiting.message ? ': ' + cur.waiting.message : ''}</span>
                      </div>
                    )}
                    {cur?.terminated && (
                      <div style={{ fontSize: 12 }}>
                        <span style={labelStyle}>当前</span>
                        <Tag color={cur.terminated.exitCode === 0 ? 'default' : 'error'} style={{ margin: 0 }}>Terminated</Tag>
                        <span style={{ marginLeft: 6, fontFamily: 'monospace', fontSize: 11, color: exitColor(cur.terminated.exitCode) }}>
                          {cur.terminated.reason} exitCode={cur.terminated.exitCode}
                        </span>
                      </div>
                    )}
                    {/* 上次退出 */}
                    {last && (
                      <div style={{ marginTop: 6, padding: '6px 10px', background: '#ff4d4f11', borderRadius: 4, borderLeft: '3px solid #ff4d4f' }}>
                        <div style={{ fontSize: 11, color: c.textHint, marginBottom: 4 }}>上次退出</div>
                        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '2px 12px', fontSize: 12 }}>
                          <span><span style={labelStyle}>原因</span><span style={{ ...valStyle, color: exitColor(last.exitCode) }}>{last.reason}</span></span>
                          <span><span style={labelStyle}>Exit Code</span><span style={{ ...valStyle, color: exitColor(last.exitCode), fontWeight: 'bold' }}>{last.exitCode}</span></span>
                          {last.signal !== undefined && <span><span style={labelStyle}>Signal</span><span style={valStyle}>{last.signal}</span></span>}
                          <span style={{ gridColumn: '1 / -1' }}><span style={labelStyle}>开始</span><span style={valStyle}>{fmtTime(last.startedAt)}</span></span>
                          <span style={{ gridColumn: '1 / -1' }}><span style={labelStyle}>结束</span><span style={valStyle}>{fmtTime(last.finishedAt)}</span></span>
                          {last.message && <span style={{ gridColumn: '1 / -1' }}><span style={labelStyle}>消息</span><span style={{ ...valStyle, color: c.danger }}>{last.message}</span></span>}
                        </div>
                      </div>
                    )}
                    {/* 资源 */}
                    {(() => {
                      const spec = [...containers, ...initContainers].find((c: any) => c.name === cs.name)
                      if (!spec?.resources) return null
                      const req = spec.resources.requests ?? {}
                      const lim = spec.resources.limits ?? {}
                      return (
                        <div style={{ marginTop: 6, fontSize: 11, color: c.textSecondary }}>
                          <span style={labelStyle}>资源</span>
                          requests: cpu={req.cpu ?? '—'} mem={req.memory ?? '—'}  | 
                          limits: cpu={lim.cpu ?? '—'} mem={lim.memory ?? '—'}
                        </div>
                      )
                    })()}
                  </div>
                )
              })}
            </div>

            {/* Pod 健康検查 */}
            {conditions.length > 0 && (
              <div style={secBg}>
                <div style={{ fontSize: 13, fontWeight: 600, marginBottom: 8, color: c.textBody }}>Pod Conditions</div>
                <Table
                  dataSource={conditions}
                  rowKey="type"
                  size="small"
                  pagination={false}
                  columns={[
                    { title: 'Type', dataIndex: 'type', width: 160, render: (v: string, r: any) => <span style={{ color: r.status === 'True' ? c.success : c.danger }}>{v}</span> },
                    { title: 'Status', dataIndex: 'status', width: 70, render: (v: string) => <Tag color={v === 'True' ? 'success' : 'error'} style={{ margin: 0 }}>{v}</Tag> },
                    { title: 'Reason', dataIndex: 'reason', render: (v: string) => <span style={{ fontSize: 11, fontFamily: 'monospace' }}>{v ?? '—'}</span> },
                    { title: 'Message', dataIndex: 'message', onCell: () => ({ style: { wordBreak: 'break-all' as const, whiteSpace: 'normal' as const } }),
                      render: (v: string) => <span style={{ fontSize: 11 }}>{v ?? '—'}</span> },
                  ]}
                />
              </div>
            )}
          </div>
        )}
      </Drawer>

      {/* 日志弹窗 */}
      <PodLogModal dsId={dsId} open={!!logModal} onClose={() => setLogModal(null)} target={logModal} />

      {/* AI 分析 */}
      <K8sAIDrawer
        open={aiOpen}
        onClose={() => setAiOpen(false)}
        resourceKind="Pod"
        namespace={target?.ns ?? ''}
        name={target?.name ?? ''}
        analysisKind="describe"
        content={describeText()}
      />
    </>
  )
}

// ─── K8sPods page ─────────────────────────────────────────────────────────────

export default function K8sPods() {
  const { data: clusters } = useClusters()
  const { dsId, select } = useSelectedCluster(clusters)
  const [searchParams, setSearchParams] = useSearchParams()
  const tabFromUrl = searchParams.get('tab') ?? 'pods'

  return (
    <>
      <PageHeader
        title="资源管理"
        extra={
          <Space>
            <ClusterSelector clusters={clusters ?? []} value={dsId} onChange={select} />
          </Space>
        }
      />
      <SurfaceCard style={{ margin: '0 24px 24px' }}>
        {!dsId && <Alert type="info" message="请先从上方选择一个集群" style={{ marginBottom: 12 }} />}
        {dsId && (
          <Tabs
            activeKey={tabFromUrl}
            onChange={key => {
              const ds = searchParams.get('ds')
              setSearchParams(ds ? { tab: key, ds } : { tab: key })
            }}
            items={[
              { key: 'pods',        label: 'Pods',        children: <PodsTab dsId={dsId} selectorFilter={searchParams.get('selector') ?? ''} nsFilter={searchParams.get('ns') ?? ''} nodeFilterProp={searchParams.get('node') ?? ''} /> },
              { key: 'deployments', label: 'Deployments', children: <DeploymentsTab dsId={dsId} /> },
              { key: 'daemonsets',  label: 'DaemonSets',  children: <DaemonSetsTab dsId={dsId} /> },
              { key: 'configmaps',  label: 'ConfigMaps',  children: <ConfigMapsTab dsId={dsId} /> },
              { key: 'volumes',     label: 'Volumes',     children: <VolumesTab dsId={dsId} /> },
              { key: 'hpas',        label: 'HPA',         children: <HpasTab dsId={dsId} /> },
              { key: 'endpoints',   label: 'Endpoints',   children: <EndpointsTab dsId={dsId} /> },
            ]}
          />
        )}
      </SurfaceCard>
    </>
  )
}

function PodLogModal({ dsId, open, onClose, target }: { dsId: string; open: boolean; onClose: () => void; target: { ns: string; name: string; container: string; restartCount?: number } | null }) {
  const preRef = useRef<HTMLPreElement>(null)
  const [aiOpen, setAiOpen] = useState(false)
  const [previous, setPrevious] = useState(false)
  const canPrevious = (target?.restartCount ?? 0) > 0
  // 关闭时重置 previous 开关
  useEffect(() => { if (!open) setPrevious(false) }, [open])
  const { data, isLoading, error } = useQuery<string>({
    queryKey: ['k8s-pod-logs', dsId, target?.ns, target?.name, target?.container, previous],
    queryFn: () => http.get<string>(`/k8s/pod/logs?ds=${dsId}&namespace=${target?.ns}&name=${target?.name}&container=${target?.container}${previous ? '&previous=true' : ''}`),
    enabled: open && !!target,
  })
  // 数据加载完后自动滚到底
  useEffect(() => {
    if (data && preRef.current) {
      preRef.current.scrollTop = preRef.current.scrollHeight
    }
  }, [data])
  return (
    <>
      <Modal
        title={
          <Space>
            <span>日志: {target?.name ?? ''} / {target?.container ?? ''}</span>
            <Switch size="small" checked={previous} onChange={v => setPrevious(v)}
              disabled={!canPrevious}
              checkedChildren="上次运行" unCheckedChildren="当前"
              title={canPrevious ? '' : '该容器未发生重启，无上次日志'} />
          </Space>
        }
        open={open} onCancel={onClose} width={900}
        footer={
          <Button icon={<RobotOutlined />} onClick={() => setAiOpen(true)} disabled={!data} style={{ color: '#722ed1', borderColor: '#722ed1' }}>
            AI 分析
          </Button>
        }
      >
        {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 8 }} />}
        <pre
          ref={preRef}
          style={{
            background: '#1e1e1e', color: '#d4d4d4', padding: 12, borderRadius: 6,
            height: 560, overflowY: 'auto', overflowX: 'hidden',
            fontSize: 12, fontFamily: 'monospace', whiteSpace: 'pre-wrap', wordBreak: 'break-all',
            margin: 0,
          }}
        >
          {isLoading ? '加载中...' : (data ?? '无日志')}
        </pre>
      </Modal>
      <K8sAIDrawer
        open={aiOpen}
        onClose={() => setAiOpen(false)}
        resourceKind="Pod"
        namespace={target?.ns ?? ''}
        name={target?.name ?? ''}
        analysisKind="logs"
        content={data ?? ''}
      />
    </>
  )
}

function PodEventModal({ dsId, open, onClose, target }: { dsId: string; open: boolean; onClose: () => void; target: { ns: string; name: string } | null }) {
  const { c } = useTheme()
  const { data, isLoading, error } = useQuery<any>({
    queryKey: ['k8s-pod-events', dsId, target?.ns, target?.name],
    queryFn: () => http.get<any>(`/k8s/pod/events?ds=${dsId}&namespace=${target?.ns}&name=${target?.name}`),
    enabled: open && !!target,
    staleTime: 0,
    refetchOnMount: 'always',
  })
  const getTs = (r: any): number => {
    const t = r.lastTimestamp || r.eventTime || r.firstTimestamp
    return t ? new Date(t).getTime() : 0
  }
  const fmtTs = (r: any): string => {
    const t = r.lastTimestamp || r.eventTime || r.firstTimestamp
    return t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '—'
  }
  const events = (data?.items ?? []).sort((a: any, b: any) => getTs(b) - getTs(a))
  const fmtEventsAsText = (evts: any[]) => evts.map((r: any) =>
    `[${fmtTs(r)}] ${r.type ?? ''} ${r.reason ?? ''}: ${r.message ?? ''}`
  ).join('\n')

  const [aiOpen, setAiOpen] = useState(false)
  return (
    <>
      <Modal
        title={`事件: ${target?.name ?? ''}`}
        open={open} onCancel={onClose} width={900}
        footer={
          <Button icon={<RobotOutlined />} onClick={() => setAiOpen(true)} disabled={events.length === 0} style={{ color: '#722ed1', borderColor: '#722ed1' }}>
            AI 分析
          </Button>
        }
      >
        {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 8 }} />}
        {isLoading ? '加载中...' : (
          events.length === 0
            ? <div style={{ textAlign: 'center', padding: '40px 0', color: c.textSecondary }}>暂无事件（K8s 事件默认保留 1 小时，Pod 正常运行时展示为空）</div>
            : <Table dataSource={events} size="small" pagination={{ pageSize: 10 }} rowKey={(r: any) => r.metadata?.uid ?? r.message}>
                <Table.Column title="时间" width={170} render={(_: any, r: any) => <span style={{ fontSize: 12 }}>{fmtTs(r)}</span>} />
                <Table.Column title="类型" width={80} render={(_: any, r: any) => <Tag color={r.type === 'Warning' ? 'warning' : r.type === 'Normal' ? 'success' : 'default'}>{r.type ?? '—'}</Tag>} />
                <Table.Column title="原因" width={140} dataIndex="reason" />
                <Table.Column title="来源" width={160} render={(_: any, r: any) => {
                  const comp = r.source?.component || r.reportingComponent || ''
                  const host = r.source?.host || r.reportingInstance || ''
                  return <span style={{ fontSize: 12 }}>{comp}{host ? '/' + host : ''}</span>
                }} />
                <Table.Column title="消息" render={(_: any, r: any) => <span style={{ fontSize: 12 }}>{r.message}</span>} />
              </Table>
        )}
      </Modal>
      <K8sAIDrawer
        open={aiOpen}
        onClose={() => setAiOpen(false)}
        resourceKind="Pod"
        namespace={target?.ns ?? ''}
        name={target?.name ?? ''}
        analysisKind="events"
        content={fmtEventsAsText(events)}
      />
    </>
  )
}

// ─── ResourceEventModal – Deployment / DaemonSet 通用事件弹窗 ────────────────────
function ResourceEventModal({
  dsId, open, onClose, target, kind,
}: {
  dsId: string
  open: boolean
  onClose: () => void
  target: { ns: string; name: string } | null
  kind: string
}) {
  const { c } = useTheme()
  const { data, isLoading, error } = useQuery<any>({
    queryKey: ['k8s-resource-events', dsId, kind, target?.ns, target?.name],
    queryFn: () => http.get<any>(`/k8s/events?ds=${dsId}&namespace=${target?.ns}&name=${target?.name}&kind=${kind}`),
    enabled: open && !!target,
    staleTime: 0,
    refetchOnMount: 'always',
  })
  const getTs = (r: any): number => {
    const t = r.lastTimestamp || r.eventTime || r.firstTimestamp
    return t ? new Date(t).getTime() : 0
  }
  const fmtTs = (r: any): string => {
    const t = r.lastTimestamp || r.eventTime || r.firstTimestamp
    return t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '—'
  }
  const events = (data?.items ?? []).sort((a: any, b: any) => getTs(b) - getTs(a))
  const fmtEventsAsText2 = (evts: any[]) => evts.map((r: any) =>
    `[${fmtTs(r)}] ${r.type ?? ''} ${r.reason ?? ''}: ${r.message ?? ''}`
  ).join('\n')
  const [aiOpen, setAiOpen] = useState(false)
  return (
    <>
      <Modal
        title={`事件: ${target?.name ?? ''}`}
        open={open} onCancel={onClose} width={900}
        footer={
          <Button icon={<RobotOutlined />} onClick={() => setAiOpen(true)} disabled={events.length === 0} style={{ color: '#722ed1', borderColor: '#722ed1' }}>
            AI 分析
          </Button>
        }
      >
        {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 8 }} />}
        {isLoading ? '加载中...' : (
          events.length === 0
            ? <div style={{ textAlign: 'center', padding: '40px 0', color: c.textSecondary }}>暂无事件（K8s 事件默认保留 1 小时，正常运行时展示为空）</div>
            : <Table dataSource={events} size="small" pagination={{ pageSize: 10 }} rowKey={(r: any) => r.metadata?.uid ?? r.message}>
                <Table.Column title="时间" width={170} render={(_: any, r: any) => <span style={{ fontSize: 12 }}>{fmtTs(r)}</span>} />
                <Table.Column title="类型" width={80} render={(_: any, r: any) => <Tag color={r.type === 'Warning' ? 'warning' : 'success'}>{r.type ?? '—'}</Tag>} />
                <Table.Column title="原因" width={140} dataIndex="reason" />
                <Table.Column title="来源" width={160} render={(_: any, r: any) => {
                  const comp = r.source?.component || r.reportingComponent || ''
                  const host = r.source?.host || r.reportingInstance || ''
                  return <span style={{ fontSize: 12 }}>{comp}{host ? '/' + host : ''}</span>
                }} />
                <Table.Column title="消息" render={(_: any, r: any) => <span style={{ fontSize: 12 }}>{r.message}</span>} />
              </Table>
        )}
      </Modal>
      <K8sAIDrawer
        open={aiOpen}
        onClose={() => setAiOpen(false)}
        resourceKind={kind}
        namespace={target?.ns ?? ''}
        name={target?.name ?? ''}
        analysisKind="events"
        content={fmtEventsAsText2(events)}
      />
    </>
  )
}

function PodExecModal({ dsId, open, onClose, target, defaultCommand }: { dsId: string; open: boolean; onClose: () => void; target: { ns: string; name: string; container: string } | null; defaultCommand?: string }) {
  const [command, setCommand] = useState(defaultCommand || 'ps aux')
  useEffect(() => {
    if (open) {
      setCommand(defaultCommand || 'ps aux')
    }
  }, [open, defaultCommand])
  const { data, isLoading, error, refetch } = useQuery<string>({
    queryKey: ['k8s-pod-exec', dsId, target?.ns, target?.name, target?.container, command],
    queryFn: () => http.post<string>(`/k8s/pod/exec?ds=${dsId}&namespace=${target?.ns}&name=${target?.name}&container=${target?.container}`, { command }),
    enabled: open && !!target,
  })
  return (
    <Modal title={`进程: ${target?.name ?? ''} / ${target?.container ?? ''}`} open={open} onCancel={onClose} width={900} footer={null}>
      <Space style={{ marginBottom: 12 }}>
        <Input value={command} onChange={e => setCommand(e.target.value)} style={{ width: 300 }} onPressEnter={() => refetch()} />
        <Button onClick={() => refetch()} loading={isLoading}>执行</Button>
      </Space>
      {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 8 }} />}
      <pre style={{ background: '#1e1e1e', color: '#d4d4d4', padding: 12, borderRadius: 6, maxHeight: 500, overflow: 'auto', fontSize: 12, fontFamily: 'monospace', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
        {isLoading ? '执行中...' : (data ?? '')}
      </pre>
    </Modal>
  )
}



// ─── PodTerminalDrawer —— xterm.js ───────────────────────────────────────────

type FileOp = 'upload' | 'download'

function PodTerminalDrawer({
  dsId, open, onClose, target
}: {
  dsId: string
  open: boolean
  onClose: () => void
  target: { ns: string; name: string; container: string } | null
}) {
  const [connected, setConnected] = useState(false)
  const [reconnectKey, setReconnectKey] = useState(0)
  const [fileModalOpen, setFileModalOpen] = useState(false)
  const [fileOp, setFileOp] = useState<FileOp>('upload')
  const [downloadPath, setDownloadPath] = useState('')
  const [uploadDestPath, setUploadDestPath] = useState('/tmp')
  const [uploading, setUploading] = useState(false)

  // 用 ref 存储内部实例，不触发重渲染
  const termRef = useRef<import('@xterm/xterm').Terminal | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const fitAddonRef = useRef<import('@xterm/addon-fit').FitAddon | null>(null)
  const destroyedRef = useRef(false)
  const containerRef = useRef<HTMLDivElement | null>(null)

  // 核心：containerRef callback，DOM 挂载时立刻初始化终端
  const initTerminal = useRef<((el: HTMLDivElement | null) => void) | null>(null)
  if (!initTerminal.current) {
    initTerminal.current = (el: HTMLDivElement | null) => {
      containerRef.current = el
    }
  }

  useEffect(() => {
    if (!open || !target) return

    destroyedRef.current = false

    // 等待 DOM 挂载（Drawer 动画完成后 DOM 才就绪）
    const timer = setTimeout(() => {
      const container = containerRef.current
      if (!container || destroyedRef.current) return

      Promise.all([
        import('@xterm/xterm'),
        import('@xterm/addon-fit'),
      ]).then(([{ Terminal }, { FitAddon }]) => {
        if (destroyedRef.current || !containerRef.current) return

        // 清理旧实例
        termRef.current?.dispose()
        wsRef.current?.close()

        const term = new Terminal({
          cursorBlink: true,
          fontSize: 14,
          fontFamily: "Menlo, Monaco, Consolas, 'Courier New', monospace",
          theme: { background: '#1e1e1e', foreground: '#d4d4d4', cursor: '#f8f8f2' },
          scrollback: 5000,
          convertEol: false,
        })
        termRef.current = term

        const fitAddon = new FitAddon()
        fitAddonRef.current = fitAddon
        term.loadAddon(fitAddon)
        term.open(containerRef.current!)
        setTimeout(() => { try { fitAddon.fit() } catch (_) {} }, 100)

        // 开发环境：直连后端 8081 绕过 Vite HMR WS 拦截
        // 生产环境：走同域（Nginx 统一代理 /api/*，需配置 proxy_set_header Upgrade）
        const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws'
        const token = useAuthStore.getState().token ?? ''
        const apiHost = import.meta.env.DEV
          ? window.location.hostname + ':8081'
          : window.location.host
        const wsUrl = `${protocol}://${apiHost}/api/v1/k8s/pod/terminal?ds=${dsId}&namespace=${target!.ns}&name=${target!.name}&container=${target!.container}&token=${encodeURIComponent(token)}`

        const ws = new WebSocket(wsUrl)
        wsRef.current = ws
        ws.binaryType = 'arraybuffer'

        ws.onopen = () => {
          if (destroyedRef.current) return
          setConnected(true)
          // 发送初始窗口尺寸
          const payload = JSON.stringify({ Width: term.cols, Height: term.rows })
          ws.send(new Uint8Array([4, ...new TextEncoder().encode(payload)]).buffer)
          // 不主动发回车，等用户输入
        }

        ws.onmessage = (evt) => {
          if (destroyedRef.current) return
          const buf = new Uint8Array(evt.data as ArrayBuffer)
          if (buf.length > 0 && (buf[0] === 1 || buf[0] === 2)) term.write(buf.slice(1))
        }

        ws.onclose = () => { if (!destroyedRef.current) { setConnected(false); term.write('\r\n\x1b[33m[连接已关闭]\x1b[0m\r\n') } }
        ws.onerror = () => { if (!destroyedRef.current) term.write('\r\n\x1b[31m[连接失败，请点重连]\x1b[0m\r\n') }

        term.onData((data) => {
          if (ws.readyState === WebSocket.OPEN)
            ws.send(new Uint8Array([0, ...new TextEncoder().encode(data)]).buffer)
        })

        term.onResize(({ cols, rows }) => {
          if (ws.readyState === WebSocket.OPEN) {
            const p = JSON.stringify({ Width: cols, Height: rows })
            ws.send(new Uint8Array([4, ...new TextEncoder().encode(p)]).buffer)
          }
        })

        const ro = new ResizeObserver(() => { try { fitAddon.fit() } catch (_) {} })
        ro.observe(containerRef.current!)
        // 清理时断开 observer
        ;(term as any).__ro = ro
      })
    }, 300) // 等待 Drawer 动画完成

    return () => {
      clearTimeout(timer)
      destroyedRef.current = true
      wsRef.current?.close()
      wsRef.current = null
      ;(termRef.current as any)?.__ro?.disconnect()
      termRef.current?.dispose()
      termRef.current = null
      fitAddonRef.current = null
      setConnected(false)
    }
  }, [open, target?.ns, target?.name, target?.container, dsId, reconnectKey])

  const handleDownload = async () => {
    if (!downloadPath || !target) return
    const token = useAuthStore.getState().token ?? ''
    const url = `/api/v1/k8s/pod/download?ds=${dsId}&namespace=${target.ns}&name=${target.name}&container=${target.container}&path=${encodeURIComponent(downloadPath)}&token=${encodeURIComponent(token)}`
    const a = document.createElement('a')
    a.href = url; a.download = downloadPath.split('/').pop() || 'file'
    document.body.appendChild(a); a.click(); document.body.removeChild(a)
    setFileModalOpen(false)
  }

  const handleUpload = async (file: File) => {
    if (!target) return false
    setUploading(true)
    const token = useAuthStore.getState().token ?? ''
    const formData = new FormData()
    formData.append('file', file)
    // 直连后端绕过 Vite 代理（Vite 代理转发 multipart 会导致 i/o timeout）
    // 后端已开启 CORS，生产环境走同域不经过此分支
    const apiBase = import.meta.env.DEV
      ? `${window.location.protocol}//${window.location.hostname}:8081`
      : ''
    try {
      const resp = await fetch(
        `${apiBase}/api/v1/k8s/pod/upload?ds=${dsId}&namespace=${target.ns}&name=${target.name}&container=${target.container}&path=${encodeURIComponent(uploadDestPath)}&token=${encodeURIComponent(token)}`,
        { method: 'POST', body: formData }
      )
      const text = await resp.text()
      let json: any = {}
      try { json = JSON.parse(text) } catch (_) { json = { code: resp.status, message: text || '上传失败' } }
      if (json.code === 0) { message.success('文件上传成功'); setFileModalOpen(false) }
      else message.error(json.message || '上传失败')
    } catch (e: any) {
      message.error(e.message || '上传失败')
    } finally {
      setUploading(false)
    }
    return false
  }

  return (
    <>
      <Drawer
        title={
          <Space>
            <span style={{ fontWeight: 600, color: '#fff' }}>{target?.name ?? ''}</span>
            <Tag color="blue">{target?.container ?? ''}</Tag>
            <Tag color={connected ? 'success' : 'default'}>{connected ? '已连接' : '未连接'}</Tag>
          </Space>
        }
        placement="right"
        width="65%"
        open={open}
        onClose={onClose}
        destroyOnClose
        closeIcon={<span style={{ color: '#fff', fontSize: 16 }}>✕</span>}
        extra={
          <Space>
            <Button size="small" icon={<UploadOutlined />}
              onClick={() => { setFileOp('upload'); setFileModalOpen(true) }}
            >上传/下载</Button>
            <Button size="small" icon={<ReloadOutlined />}
              onClick={() => {
                wsRef.current?.close()
                wsRef.current = null
                setReconnectKey(k => k + 1)
              }}
            >重连</Button>
          </Space>
        }
        styles={{
          body: { padding: 0, background: '#1e1e1e', display: 'flex', flexDirection: 'column', overflow: 'hidden' },
          header: { background: '#252526', borderBottom: '1px solid #3c3c3c', color: '#fff' },
        }}
      >
        <div
          ref={initTerminal.current}
          style={{ flex: 1, minHeight: 0, width: '100%' }}
        />
      </Drawer>

      <Modal
        title="文件上传 / 下载"
        open={fileModalOpen}
        onCancel={() => setFileModalOpen(false)}
        footer={null}
        width={480}
        zIndex={1100}
      >
        <div style={{ marginBottom: 16 }}>
          <Radio.Group value={fileOp} onChange={e => setFileOp(e.target.value)}>
            <Radio.Button value="upload"><UploadOutlined /> 上传文件</Radio.Button>
            <Radio.Button value="download"><DownloadOutlined /> 下载文件</Radio.Button>
          </Radio.Group>
        </div>

        {fileOp === 'upload' && (
          <Space direction="vertical" style={{ width: '100%' }}>
            <div>
              <span style={{ marginRight: 8 }}>目标路径：</span>
              <Input value={uploadDestPath} onChange={e => setUploadDestPath(e.target.value)}
                placeholder="如 /tmp" style={{ width: 280 }} />
            </div>
            <Upload beforeUpload={handleUpload} showUploadList={false} disabled={uploading}>
              <Button icon={<UploadOutlined />} loading={uploading}>选择并上传文件</Button>
            </Upload>
            <span style={{ color: '#888', fontSize: 12 }}>文件将上传到容器的指定目录</span>
          </Space>
        )}

        {fileOp === 'download' && (
          <Space direction="vertical" style={{ width: '100%' }}>
            <div>
              <span style={{ marginRight: 8 }}>容器内文件路径：</span>
              <Input value={downloadPath} onChange={e => setDownloadPath(e.target.value)}
                placeholder="如 /tmp/app.log" style={{ width: 280 }} />
            </div>
            <Button icon={<DownloadOutlined />} onClick={handleDownload} disabled={!downloadPath}>
              下载文件
            </Button>
          </Space>
        )}
      </Modal>
    </>
  )
}
