/**
 * K8sNodes – 节点管理
 * 标签查看/编辑、污点查看/编辑、停止/加入调度、删除
 */
import { useState, useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Table, Tag, Space, Alert, Typography, Button, Tooltip, Popconfirm, message,
  Modal, Input,
} from 'antd'
import {
  ReloadOutlined, DeleteOutlined, EyeOutlined,
  StopOutlined, PlayCircleOutlined, TagsOutlined, WarningOutlined, UnorderedListOutlined, RobotOutlined, LinkOutlined,
} from '@ant-design/icons'
import { PageHeader } from '../../components/PageHeader'
import { SurfaceCard } from '../../components/SurfaceCard'
import { useTheme } from '../../hooks/useTheme'
import { http } from '../../api/request'
import { useClusters, useSelectedCluster, fmtCreation } from './useCluster'
import { ClusterSelector } from './ClusterSelector'
import { YamlEditor } from './YamlEditor'
import { K8sAIDrawer } from './K8sAIDrawer'

const { Text } = Typography

interface NodeCondition { type: string; status: string }
interface NodeAddress { type: string; address: string }
interface NodeCapacity { cpu?: unknown; memory?: unknown; pods?: unknown }
interface Taint { key: string; value?: string; effect: string }
interface NodeItem {
  metadata?: { name?: string; labels?: Record<string, string>; creationTimestamp?: string }
  spec?: { unschedulable?: boolean; taints?: Taint[] }
  status?: {
    conditions?: NodeCondition[]
    addresses?: NodeAddress[]
    capacity?: NodeCapacity
    allocatable?: NodeCapacity
    nodeInfo?: { kubeletVersion?: string; osImage?: string; containerRuntimeVersion?: string }
  }
}
interface NodeList { items?: NodeItem[] }

interface K8sEvent {
  metadata?: { name?: string; creationTimestamp?: string }
  type?: string
  reason?: string
  message?: string
  count?: number
  firstTimestamp?: string
  lastTimestamp?: string
  involvedObject?: { kind?: string; name?: string }
}

// ─── Node Events Modal ────────────────────────────────────────────────────────

function NodeEventsModal({ dsId, nodeName, open, onClose }: {
  dsId: string; nodeName: string; open: boolean; onClose: () => void
}) {
  const { c } = useTheme()
  const [aiOpen, setAiOpen] = useState(false)
  const { data, isLoading, error } = useQuery<{ items?: K8sEvent[] }>({
    queryKey: ['k8s-node-events', dsId, nodeName],
    queryFn: () => http.get('/k8s/events', { params: { ds: dsId, name: nodeName, kind: 'Node' } }),
    enabled: open && !!nodeName,
    staleTime: 0,
  })

  const events = (data?.items ?? []).sort((a, b) =>
    new Date(b.lastTimestamp ?? b.metadata?.creationTimestamp ?? 0).getTime() -
    new Date(a.lastTimestamp ?? a.metadata?.creationTimestamp ?? 0).getTime()
  )

  const fmtTime = (t?: string) => t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '—'

  // 生成 AI 分析用的文本
  const eventsText = events.map(e =>
    `[${e.type}] ${e.reason ?? ''} ×${e.count ?? 1}  ${e.message ?? ''}  @${fmtTime(e.lastTimestamp)}`
  ).join('\n')

  return (
    <>
      <Modal
        title={`事件 — ${nodeName}`}
        open={open}
        onCancel={onClose}
        footer={
          <Button icon={<RobotOutlined />} onClick={() => setAiOpen(true)}
            disabled={events.length === 0}
            style={{ color: '#722ed1', borderColor: '#722ed1' }}>
            AI 分析
          </Button>
        }
        width={900}
      >
        {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 8 }} />}
        <Table
          dataSource={events}
          rowKey={e => e.metadata?.name ?? Math.random().toString()}
          loading={isLoading}
          size="small"
          pagination={false}
          scroll={{ y: 420 }}
          columns={[
            {
              title: '类型', dataIndex: 'type', width: 70,
              render: (v: string) => <Tag color={v === 'Warning' ? 'warning' : 'success'} style={{ margin: 0 }}>{v}</Tag>,
            },
            { title: '原因', dataIndex: 'reason', width: 140, render: (v: string) => <span style={{ fontSize: 12, fontFamily: 'monospace' }}>{v}</span> },
            { title: '次数', dataIndex: 'count', width: 55, render: (v: number) => <span style={{ fontSize: 12, color: v > 1 ? c.warning : c.textSecondary }}>{v ?? 1}</span> },
            { title: '消息', dataIndex: 'message', render: (v: string) => <span style={{ fontSize: 12, wordBreak: 'break-all' }}>{v}</span> },
            { title: '最近时间', width: 160, render: (_: unknown, e: K8sEvent) => <span style={{ fontSize: 11, color: c.textSecondary }}>{fmtTime(e.lastTimestamp)}</span> },
          ]}
        />
      </Modal>
      <K8sAIDrawer
        open={aiOpen}
        onClose={() => setAiOpen(false)}
        resourceKind="Node"
        namespace=""
        name={nodeName}
        analysisKind="events"
        content={eventsText}
      />
    </>
  )
}

function isNodeReady(node: NodeItem): boolean {
  return (node.status?.conditions ?? []).some(c => c.type === 'Ready' && c.status === 'True')
}

function nodeInternalIP(node: NodeItem): string {
  return (node.status?.addresses ?? []).find(a => a.type === 'InternalIP')?.address ?? '—'
}

function nodeRole(node: NodeItem): string {
  const labels = node.metadata?.labels ?? {}
  if ('node-role.kubernetes.io/control-plane' in labels || 'node-role.kubernetes.io/master' in labels) return 'control-plane'
  return 'worker'
}

// K8s resource.Quantity 可能是字符串或带 string 字段的对象
function quantityStr(v: unknown): string {
  if (!v && v !== 0) return '—'
  if (typeof v === 'string') return v
  if (typeof v === 'object' && v !== null) {
    const obj = v as Record<string, unknown>
    if ('string' in obj) return String(obj.string)
    if ('value' in obj) return String(obj.value)
  }
  return String(v)
}

function fmtMem(raw: unknown): string {
  const s = quantityStr(raw)
  if (s === '—') return s
  // K8s memory format: e.g. "16384Mi", "16Gi", "32767512Ki"
  const match = s.match(/^([\d.]+)(Ki|Mi|Gi|Ti)$/)
  if (!match) return s
  const num = parseFloat(match[1])
  const unit = match[2]
  if (unit === 'Ki') {
    const mi = num / 1024
    if (mi >= 1024) return `${(mi / 1024).toFixed(1)}Gi`
    return `${mi.toFixed(0)}Mi`
  }
  return `${num}${unit}`
}

// ─── Label Editor Modal ───────────────────────────────────────────────────────

function LabelEditorModal({ node, dsId, open, onClose }: {
  node: NodeItem | null; dsId: string; open: boolean; onClose: () => void
}) {
  const qc = useQueryClient()
  const [labels, setLabels] = useState<Record<string, string>>({})
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')

  // Load labels when modal opens
  const { data: nodeDetail } = useQuery<unknown>({
    queryKey: ['k8s-node-detail', dsId, node?.metadata?.name],
    queryFn: () => http.get<unknown>('/k8s/node', { params: { ds: dsId, name: node!.metadata!.name! } }),
    enabled: open && !!node,
    staleTime: 0,
  })

  const currentLabels = (nodeDetail as NodeItem)?.metadata?.labels ?? node?.metadata?.labels ?? {}
  const displayLabels = Object.keys(currentLabels).length > 0 ? { ...currentLabels, ...labels } : labels

  const updateMut = useMutation({
    mutationFn: (body: string) =>
      http.put(`/k8s/node?ds=${dsId}&name=${node?.metadata?.name}`, JSON.parse(body)),
    onSuccess: () => { message.success('标签已更新'); qc.invalidateQueries({ queryKey: ['k8s-nodes', dsId] }); onClose() },
    onError: (e: Error) => message.error(e.message),
  })

  const handleSave = () => {
    const base = nodeDetail ?? node
    const updated = { ...(base as Record<string, unknown>), metadata: { ...(base as NodeItem).metadata, labels: displayLabels } }
    updateMut.mutate(JSON.stringify(updated))
  }

  const addLabel = () => {
    if (!newKey.trim()) return
    setLabels(prev => ({ ...prev, [newKey.trim()]: newVal.trim() }))
    setNewKey('')
    setNewVal('')
  }

  const removeLabel = (key: string) => {
    setLabels(prev => {
      const next = { ...prev }
      delete next[key]
      return next
    })
  }

  return (
    <Modal
      title={`标签编辑 — ${node?.metadata?.name ?? ''}`}
      open={open}
      onCancel={onClose}
      onOk={handleSave}
      confirmLoading={updateMut.isPending}
      okText="保存"
      width={680}
    >
      <Space style={{ marginBottom: 12, width: '100%' }} direction="vertical">
        <Space>
          <Input placeholder="标签键 (如: env)" value={newKey} onChange={e => setNewKey(e.target.value)} style={{ width: 200 }} />
          <Input placeholder="标签值 (如: production)" value={newVal} onChange={e => setNewVal(e.target.value)} style={{ width: 200 }} />
          <Button onClick={addLabel} disabled={!newKey.trim()}>添加</Button>
        </Space>
      </Space>
      <div style={{ maxHeight: 400, overflow: 'auto' }}>
        {Object.entries(displayLabels).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => (
          <div key={k} style={{ display: 'flex', alignItems: 'center', padding: '4px 0', borderBottom: '1px solid #f0f0f0' }}>
            <Text style={{ fontFamily: 'monospace', fontSize: 12, flex: 1 }}>{k}</Text>
            <Text style={{ fontSize: 12, color: '#888', flex: 1 }}>{v}</Text>
            <Button size="small" type="text" danger onClick={() => removeLabel(k)}>删除</Button>
          </div>
        ))}
      </div>
    </Modal>
  )
}

// ─── Taint Editor Modal ───────────────────────────────────────────────────────

function TaintEditorModal({ node, dsId, open, onClose }: {
  node: NodeItem | null; dsId: string; open: boolean; onClose: () => void
}) {
  const qc = useQueryClient()
  const [taints, setTaints] = useState<Taint[]>([])
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')
  const [newEffect, setNewEffect] = useState('NoSchedule')

  const { data: nodeDetail } = useQuery<unknown>({
    queryKey: ['k8s-node-taint-detail', dsId, node?.metadata?.name],
    queryFn: () => http.get<unknown>('/k8s/node', { params: { ds: dsId, name: node!.metadata!.name! } }),
    enabled: open && !!node,
    staleTime: 0,
  })

  const currentTaints = (nodeDetail as NodeItem)?.spec?.taints ?? node?.spec?.taints ?? []
  const displayTaints = taints.length > 0 ? taints : currentTaints

  const updateMut = useMutation({
    mutationFn: (body: string) =>
      http.put(`/k8s/node?ds=${dsId}&name=${node?.metadata?.name}`, JSON.parse(body)),
    onSuccess: () => { message.success('污点已更新'); qc.invalidateQueries({ queryKey: ['k8s-nodes', dsId] }); onClose() },
    onError: (e: Error) => message.error(e.message),
  })

  const handleSave = () => {
    const base = nodeDetail ?? node
    const updated = { ...base as Record<string, unknown>, spec: { ...(base as NodeItem).spec, taints: displayTaints } }
    updateMut.mutate(JSON.stringify(updated))
  }

  const addTaint = () => {
    if (!newKey.trim()) return
    setTaints(prev => [...prev, { key: newKey.trim(), value: newVal.trim(), effect: newEffect }])
    setNewKey('')
    setNewVal('')
  }

  const removeTaint = (idx: number) => {
    setTaints(prev => prev.filter((_, i) => i !== idx))
  }

  const effectColor: Record<string, string> = {
    NoSchedule: 'red', NoExecute: 'volcano', PreferNoSchedule: 'orange',
  }

  return (
    <Modal
      title={`污点编辑 — ${node?.metadata?.name ?? ''}`}
      open={open}
      onCancel={onClose}
      onOk={handleSave}
      confirmLoading={updateMut.isPending}
      okText="保存"
      width={680}
    >
      <Alert type="warning" message="污点修改会影响 Pod 调度，请谨慎操作" showIcon style={{ marginBottom: 12 }} />
      <Space style={{ marginBottom: 12 }} wrap>
        <Input placeholder="污点键 (如: key)" value={newKey} onChange={e => setNewKey(e.target.value)} style={{ width: 150 }} />
        <Input placeholder="污点值 (可选)" value={newVal} onChange={e => setNewVal(e.target.value)} style={{ width: 150 }} />
        <select value={newEffect} onChange={e => setNewEffect(e.target.value)} style={{ width: 130, height: 32, border: '1px solid #d9d9d9', borderRadius: 6 }}>
          <option value="NoSchedule">NoSchedule</option>
          <option value="PreferNoSchedule">PreferNoSchedule</option>
          <option value="NoExecute">NoExecute</option>
        </select>
        <Button onClick={addTaint} disabled={!newKey.trim()}>添加</Button>
      </Space>
      <div style={{ maxHeight: 300, overflow: 'auto' }}>
        {displayTaints.map((t, idx) => (
          <div key={idx} style={{ display: 'flex', alignItems: 'center', padding: '4px 0', borderBottom: '1px solid #f0f0f0' }}>
            <Text style={{ fontFamily: 'monospace', fontSize: 12, flex: 1 }}>{t.key}{t.value ? `=${t.value}` : ''}</Text>
            <Tag color={effectColor[t.effect] ?? 'default'} style={{ margin: 0 }}>{t.effect}</Tag>
            <Button size="small" type="text" danger onClick={() => removeTaint(idx)} style={{ marginLeft: 8 }}>删除</Button>
          </div>
        ))}
        {displayTaints.length === 0 && <div style={{ textAlign: 'center', color: '#999', padding: 20 }}>无污点</div>}
      </div>
    </Modal>
  )
}

// ─── Main Component ───────────────────────────────────────────────────────────

export default function K8sNodes() {
  const { c } = useTheme()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const { data: clusters } = useClusters()
  const { dsId, select } = useSelectedCluster(clusters)

  const [labelNode, setLabelNode] = useState<NodeItem | null>(null)
  const [taintNode, setTaintNode] = useState<NodeItem | null>(null)
  const [viewTarget, setViewTarget] = useState<string | null>(null)
  const [eventNode, setEventNode] = useState<string | null>(null)
  const [search, setSearch] = useState('')

  const { data, isLoading, error, refetch } = useQuery<NodeList>({
    queryKey: ['k8s-nodes', dsId],
    queryFn: () => http.get<NodeList>('/k8s/nodes', { params: { ds: dsId, pageSize: -1 } }),
    enabled: !!dsId,
    staleTime: 15_000,
    refetchInterval: 30_000,
  })

  const filteredNodes = useMemo(() => {
    const items = data?.items ?? []
    if (!search) return items
    const lower = search.toLowerCase()
    return items.filter(n =>
      (n.metadata?.name ?? '').toLowerCase().includes(lower) ||
      nodeInternalIP(n).includes(lower)
    )
  }, [data, search])

  const { data: nodeDetail, isFetching: nodeFetching } = useQuery<unknown>({
    queryKey: ['k8s-node-view', dsId, viewTarget],
    queryFn: () => http.get<unknown>('/k8s/node', { params: { ds: dsId, name: viewTarget! } }),
    enabled: !!viewTarget,
    staleTime: 0,
  })

  const cordonMut = useMutation({
    mutationFn: (name: string) => http.post(`/k8s/node/cordon?ds=${dsId}&name=${name}`),
    onSuccess: () => { message.success('节点已停止调度'); qc.invalidateQueries({ queryKey: ['k8s-nodes', dsId] }) },
    onError: (e: Error) => message.error(e.message),
  })

  const uncordonMut = useMutation({
    mutationFn: (name: string) => http.post(`/k8s/node/uncordon?ds=${dsId}&name=${name}`),
    onSuccess: () => { message.success('节点已恢复调度'); qc.invalidateQueries({ queryKey: ['k8s-nodes', dsId] }) },
    onError: (e: Error) => message.error(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: (name: string) => http.delete(`/k8s/node?ds=${dsId}&name=${name}`),
    onSuccess: () => { message.success('节点已删除'); qc.invalidateQueries({ queryKey: ['k8s-nodes', dsId] }) },
    onError: (e: Error) => message.error(e.message),
  })

  const columns = [
    {
      title: '节点名称',
      render: (_: unknown, n: NodeItem) => (
        <Space size={4}>
          <Text style={{ fontFamily: 'monospace', fontSize: 12 }}>{n.metadata?.name}</Text>
          {n.spec?.unschedulable && <Tag color="orange" style={{ margin: 0, fontSize: 10 }}>SchedulingDisabled</Tag>}
        </Space>
      ),
    },
    {
      title: '状态', width: 90,
      render: (_: unknown, n: NodeItem) => isNodeReady(n)
        ? <Tag color="success" style={{ margin: 0 }}>Ready</Tag>
        : <Tag color="error" style={{ margin: 0 }}>NotReady</Tag>,
    },
    {
      title: '角色', width: 100,
      render: (_: unknown, n: NodeItem) => {
        const role = nodeRole(n)
        return role === 'control-plane'
          ? <Tag color="purple" style={{ margin: 0 }}>{role}</Tag>
          : <Tag color="blue" style={{ margin: 0 }}>{role}</Tag>
      },
    },
    {
      title: 'IP', width: 130,
      render: (_: unknown, n: NodeItem) => (
        <span style={{ fontSize: 12, fontFamily: 'monospace', color: c.textSecondary }}>{nodeInternalIP(n)}</span>
      ),
    },
    {
      title: '标签', width: 220,
      render: (_: unknown, n: NodeItem) => {
        const labels = n.metadata?.labels ?? {}
        // Filter out k8s internal labels (node-role, kubernetes.io/*)
        const showLabels = Object.entries(labels).filter(([k]) =>
          !k.startsWith('node-role.kubernetes.io/') &&
          !k.startsWith('kubernetes.io/') &&
          k !== 'beta.kubernetes.io/arch' &&
          k !== 'beta.kubernetes.io/os'
        )
        if (showLabels.length === 0) return <span style={{ color: c.textSecondary }}>无</span>
        return (
          <Space size={4} wrap>
            {showLabels.slice(0, 2).map(([k, v], i) => (
              <Tag key={i} style={{ margin: 0, fontSize: 11 }}>{k}={v}</Tag>
            ))}
            {showLabels.length > 2 && <Tag style={{ margin: 0 }}>+{showLabels.length - 2}</Tag>}
          </Space>
        )
      },
    },
    {
      title: 'CPU/内存', width: 140,
      render: (_: unknown, n: NodeItem) => (
        <span style={{ fontSize: 12, color: c.textSecondary }}>{quantityStr(n.status?.capacity?.cpu)} / {fmtMem(n.status?.capacity?.memory)}</span>
      ),
    },
    {
      title: 'Kubelet', width: 120,
      render: (_: unknown, n: NodeItem) => (
        <span style={{ fontSize: 12, color: c.textSecondary }}>{n.status?.nodeInfo?.kubeletVersion ?? '—'}</span>
      ),
    },
    {
      title: '创建时间', width: 110,
      render: (_: unknown, n: NodeItem) => {
        const { date } = fmtCreation(n.metadata?.creationTimestamp)
        return <span style={{ fontSize: 11, color: c.textSecondary }}>{date}</span>
      },
    },
    {
      title: '运行时长', width: 90,
      render: (_: unknown, n: NodeItem) => {
        const { age } = fmtCreation(n.metadata?.creationTimestamp)
        return <span style={{ fontSize: 11, color: c.textHint }}>{age}</span>
      },
    },
    {
      title: '操作', width: 210, fixed: 'right' as const,
      render: (_: unknown, n: NodeItem) => {
        const name = n.metadata?.name ?? ''
        const isSchedulingDisabled = n.spec?.unschedulable === true
        return (
          <Space size={4}>
            <Tooltip title="查看 Pod">
              <Button size="small" type="text" icon={<LinkOutlined />}
                onClick={() => navigate(`/k8s/resources?tab=pods&node=${encodeURIComponent(name)}`)} />
            </Tooltip>
            <Tooltip title="编辑标签">
              <Button size="small" type="text" icon={<TagsOutlined />}
                onClick={() => setLabelNode(n)} />
            </Tooltip>
            <Tooltip title="编辑污点">
              <Button size="small" type="text" icon={<WarningOutlined />}
                onClick={() => setTaintNode(n)} />
            </Tooltip>
            <Tooltip title="事件">
              <Button size="small" type="text" icon={<UnorderedListOutlined />}
                onClick={() => setEventNode(name)} />
            </Tooltip>
            <Tooltip title="查看 JSON">
              <Button size="small" type="text" icon={<EyeOutlined />}
                loading={nodeFetching && viewTarget === name}
                onClick={() => setViewTarget(name)} />
            </Tooltip>
            {isSchedulingDisabled ? (
              <Tooltip title="恢复调度">
                <Button size="small" type="text" icon={<PlayCircleOutlined />}
                  style={{ color: '#52c41a' }}
                  onClick={() => uncordonMut.mutate(name)} loading={uncordonMut.isPending} />
              </Tooltip>
            ) : (
              <Popconfirm
                title={`确认停止调度节点 "${name}"？`}
                description="停止调度后，新 Pod 将不会调度到该节点"
                okText="停止调度" cancelText="取消"
                onConfirm={() => cordonMut.mutate(name)}
              >
                <Tooltip title="停止调度">
                  <Button size="small" type="text" icon={<StopOutlined />}
                    style={{ color: '#faad14' }} loading={cordonMut.isPending && cordonMut.variables === name} />
                </Tooltip>
              </Popconfirm>
            )}
            <Popconfirm
              title={`确认删除节点 "${name}"？`}
              description="此操作不可逆，节点将从集群中移除"
              okText="删除" cancelText="取消" okButtonProps={{ danger: true }}
              onConfirm={() => deleteMut.mutate(name)}
            >
              <Tooltip title="删除节点">
                <Button size="small" type="text" danger icon={<DeleteOutlined />} loading={deleteMut.isPending} />
              </Tooltip>
            </Popconfirm>
          </Space>
        )
      },
    },
  ]

  return (
    <>
      <PageHeader
        title="节点管理"
        extra={
          <ClusterSelector clusters={clusters ?? []} value={dsId} onChange={select} />
        }
      />
      <SurfaceCard style={{ margin: '0 24px 24px' }}>
        {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 12 }} />}
        {!dsId && <Alert type="info" message="请先从上方选择一个集群" />}
        {dsId && (
          <>
            <div style={{ marginBottom: 12 }}>
              <Space>
                <Input.Search
                  placeholder="搜索节点名/IP"
                  allowClear
                  value={search}
                  onChange={e => setSearch(e.target.value)}
                  style={{ width: 260 }}
                />
                <Button icon={<ReloadOutlined />} onClick={() => refetch()} loading={isLoading}>刷新</Button>
              </Space>
            </div>
            <Table
              dataSource={filteredNodes}
              columns={columns}
              rowKey={n => n.metadata?.name ?? ''}
              loading={isLoading}
              size="small"
              scroll={{ x: 'max-content' }}
              pagination={{ pageSize: 20, showSizeChanger: true, showTotal: t => `共 ${t} 个节点` }}
            />
          </>
        )}
      </SurfaceCard>

      {/* Node Events */}
      <NodeEventsModal dsId={dsId} nodeName={eventNode ?? ''} open={!!eventNode} onClose={() => setEventNode(null)} />

      {/* Label Editor */}
      <LabelEditorModal node={labelNode} dsId={dsId} open={!!labelNode} onClose={() => setLabelNode(null)} />

      {/* Taint Editor */}
      <TaintEditorModal node={taintNode} dsId={dsId} open={!!taintNode} onClose={() => setTaintNode(null)} />

      {/* JSON Viewer */}
      <YamlEditor
        title={`查看节点: ${viewTarget ?? ''}`}
        value={nodeDetail}
        open={!!viewTarget && !!nodeDetail}
        onClose={() => setViewTarget(null)}
        loading={nodeFetching}
      />
    </>
  )
}