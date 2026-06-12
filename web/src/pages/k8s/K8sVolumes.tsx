/**
 * K8sVolumes – PVC 列表，含扩容 + 查看 JSON（Tab 组件）
 */
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Table, Tag, Space, Alert, Typography, Input, Button, Modal, Form, Tooltip, message, Select, Switch, InputNumber } from 'antd'
import { ReloadOutlined, EyeOutlined, ExpandAltOutlined, UnorderedListOutlined } from '@ant-design/icons'
import { useTheme } from '../../hooks/useTheme'
import { http } from '../../api/request'
import { byCreation, k8sPagination, fmtCreation, useNamespaces, useAutoRefresh, useClusters, useSelectedCluster, useK8sList } from './useCluster'
import { PageHeader } from '../../components/PageHeader'
import { SurfaceCard } from '../../components/SurfaceCard'
import { ClusterSelector } from './ClusterSelector'
import { YamlEditor } from './YamlEditor'
import { K8sAIDrawer } from './K8sAIDrawer'

const { Text } = Typography

interface PVCItem {
  metadata?: { name?: string; namespace?: string; creationTimestamp?: string }
  spec?: {
    accessModes?: string[]
    storageClassName?: string
    resources?: { requests?: { storage?: unknown } }
    volumeName?: string
  }
  status?: { phase?: string; capacity?: { storage?: unknown } }
}

// K8s Quantity 可能是字符串或对象，统一转成字符串
function storageStr(v: unknown): string {
  if (!v) return '—'
  if (typeof v === 'string') return v
  if (typeof v === 'object' && v !== null && 'string' in v) return String((v as Record<string, unknown>).string)
  return String(v)
}

const phaseColor: Record<string, string> = {
  Bound: 'success', Pending: 'warning', Lost: 'error',
}

export function VolumesTab({ dsId }: { dsId: string }) {
  const { c } = useTheme()
  const qc = useQueryClient()
  const [resizeTarget, setResizeTarget] = useState<PVCItem | null>(null)
  const [resizeForm] = Form.useForm()
  const [viewTarget, setViewTarget] = useState<{ ns: string; name: string } | null>(null)
  const [eventTarget, setEventTarget] = useState<{ ns: string; name: string } | null>(null)
  const [aiOpen, setAiOpen] = useState(false)
  const [aiContent, setAiContent] = useState('')

  const {
    data: items,
    isLoading,
    error,
    refetch,
    pagination,
    search,
    doSearch,
    namespace: ns,
    setNamespace: setNs,
  } = useK8sList<PVCItem>('/k8s/pvcs', {
    dsId,
    pageSize: 20,
    searchDelay: 300,
  })

  const { data: namespaces = [] } = useNamespaces(dsId)
  const autoRefresh = useAutoRefresh(() => refetch(), 0)

  const { data: pvcDetail, isFetching: pvcFetching } = useQuery<unknown>({
    queryKey: ['k8s-pvc-detail', dsId, viewTarget?.ns, viewTarget?.name],
    queryFn: () => http.get<unknown>('/k8s/pvc', {
      params: { ds: dsId, namespace: viewTarget!.ns, name: viewTarget!.name },
    }),
    enabled: !!viewTarget,
    staleTime: 0,
  })

  const resizeMut = useMutation({
    mutationFn: ({ ns, name, storage }: { ns: string; name: string; storage: string }) =>
      http.post(`/k8s/pvc/resize?ds=${dsId}&namespace=${ns}&name=${name}`, { storage }),
    onSuccess: () => {
      message.success('扩容请求已提交')
      setResizeTarget(null)
      qc.invalidateQueries({ queryKey: ['k8s-pvcs', dsId] })
    },
    onError: (e: Error) => message.error(e.message),
  })

  const sortedItems = (items ?? []).filter(Boolean).sort(byCreation)

  const columns = [
    { title: 'PVC 名称', render: (_: unknown, p: PVCItem) => <Text style={{ fontFamily: 'monospace', fontSize: 12 }}>{p.metadata?.name}</Text> },
    { title: '命名空间', width: 140, render: (_: unknown, p: PVCItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{p.metadata?.namespace}</span> },
    { title: '状态', width: 90, render: (_: unknown, p: PVCItem) => <Tag color={phaseColor[p.status?.phase ?? ''] ?? 'default'} style={{ margin: 0 }}>{p.status?.phase || '—'}</Tag> },
    { title: '容量', width: 100, render: (_: unknown, p: PVCItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{storageStr(p.status?.capacity?.storage) !== '—' ? storageStr(p.status?.capacity?.storage) : storageStr(p.spec?.resources?.requests?.storage)}</span> },
    { title: '申请容量', width: 100, render: (_: unknown, p: PVCItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{storageStr(p.spec?.resources?.requests?.storage)}</span> },
    { title: '访问模式', width: 160, render: (_: unknown, p: PVCItem) => <span style={{ fontSize: 11, color: c.textSecondary }}>{(p.spec?.accessModes ?? []).map(String).join(', ')}</span> },
    { title: 'StorageClass', width: 150, render: (_: unknown, p: PVCItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{p.spec?.storageClassName ?? '—'}</span> },
    { title: '绑定卷', render: (_: unknown, p: PVCItem) => <span style={{ fontSize: 12, fontFamily: 'monospace', color: c.textSecondary }}>{p.spec?.volumeName ?? '—'}</span> },
    { title: '创建时间', width: 110, render: (_: unknown, p: PVCItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{fmtCreation(p.metadata?.creationTimestamp).date}</span> },
    { title: '运行时长', width: 90, render: (_: unknown, p: PVCItem) => <span style={{ fontSize: 12, color: c.textHint }}>{fmtCreation(p.metadata?.creationTimestamp).age}</span> },
    {
      title: '操作', width: 130, fixed: 'right' as const,
      render: (_: unknown, p: PVCItem) => (
        <Space size={4}>
          <Tooltip title="查看事件">
            <Button size="small" type="text" icon={<UnorderedListOutlined />}
              onClick={() => setEventTarget({ ns: p.metadata?.namespace ?? '', name: p.metadata?.name ?? '' })} />
          </Tooltip>
          <Tooltip title="扩容">
            <Button size="small" type="text" icon={<ExpandAltOutlined />}
              onClick={() => { setResizeTarget(p); resizeForm.setFieldsValue({ storage: storageStr(p.spec?.resources?.requests?.storage) !== '—' ? storageStr(p.spec?.resources?.requests?.storage) : storageStr(p.status?.capacity?.storage) }) }} />
          </Tooltip>
          <Tooltip title="查看 JSON">
            <Button size="small" type="text" icon={<EyeOutlined />}
              loading={pvcFetching && viewTarget?.name === p.metadata?.name}
              onClick={() => setViewTarget({ ns: p.metadata?.namespace ?? '', name: p.metadata?.name ?? '' })} />
          </Tooltip>
        </Space>
      ),
    },
  ]

  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <Select style={{ width: 240 }} placeholder="命名空间（全部）" allowClear showSearch
          value={ns || undefined} onChange={v => setNs(v ?? '')}
          options={namespaces.map(n => ({ label: n, value: n }))} />
        <Input.Search placeholder="搜索 PVC 名称" allowClear style={{ width: 220 }}
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
      {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 12 }} />}
      <Table dataSource={sortedItems} columns={columns}
        rowKey={p => `${p.metadata?.namespace}/${p.metadata?.name}`}
        loading={isLoading} size="small" scroll={{ x: 'max-content' }}
        pagination={pagination} />

      <Modal title={`扩容 PVC — ${resizeTarget?.metadata?.name}`}
        open={!!resizeTarget} onCancel={() => setResizeTarget(null)}
        confirmLoading={resizeMut.isPending} okText="确认扩容"
        onOk={() => {
          resizeForm.validateFields().then(v => {
            resizeMut.mutate({ ns: resizeTarget?.metadata?.namespace ?? '', name: resizeTarget?.metadata?.name ?? '', storage: v.storage })
          })
        }}>
        <Alert type="warning" message="PVC 只能扩容不能缩容，实际生效取决于存储类是否支持动态扩容" showIcon style={{ marginBottom: 16 }} />
        <Form form={resizeForm} layout="vertical">
          <Form.Item name="storage" label="新的存储容量" rules={[
            { required: true, message: '请输入容量' },
            { validator: (_, v) => /^\d+(\.\d+)?(Ki|Mi|Gi|Ti|Pi|Ei|m|k|M|G|T|P|E)?$/.test(v) ? Promise.resolve() : Promise.reject(new Error('请输入有效容量格式')) },
          ]}>
            <Input placeholder="如：20Gi、500Mi" style={{ width: '100%' }} />
          </Form.Item>
          <div style={{ fontSize: 12, color: '#888', marginTop: -8 }}>
            当前容量：{storageStr(resizeTarget?.status?.capacity?.storage) !== '—' ? storageStr(resizeTarget?.status?.capacity?.storage) : storageStr(resizeTarget?.spec?.resources?.requests?.storage)}
          </div>
        </Form>
      </Modal>

      <YamlEditor title={`查看 PVC: ${viewTarget?.name ?? ''}`}
        value={pvcDetail} open={!!viewTarget && !!pvcDetail}
        onClose={() => setViewTarget(null)} loading={pvcFetching} />

      <PvcEventsModal dsId={dsId} target={eventTarget} open={!!eventTarget} onClose={() => setEventTarget(null)}
        onAiOpen={(text) => { setAiContent(text); setAiOpen(true) }} />
      <K8sAIDrawer open={aiOpen} onClose={() => setAiOpen(false)}
        resourceKind="PersistentVolumeClaim" namespace={eventTarget?.ns ?? ''} name={eventTarget?.name ?? ''}
        analysisKind="events" content={aiContent} />
    </>
  )
}

function PvcEventsModal({ dsId, target, open, onClose, onAiOpen }: {
  dsId: string; target: { ns: string; name: string } | null; open: boolean; onClose: () => void
  onAiOpen: (text: string) => void
}) {
  const { c } = useTheme()
  const { data, isLoading, error } = useQuery<{ items?: any[] }>({
    queryKey: ['k8s-pvc-events', dsId, target?.ns, target?.name],
    queryFn: () => http.get('/k8s/events', { params: { ds: dsId, namespace: target?.ns, name: target?.name, kind: 'PersistentVolumeClaim' } }),
    enabled: open && !!target,
    staleTime: 0,
  })
  const events = (data?.items ?? []).sort((a: any, b: any) =>
    new Date(b.lastTimestamp ?? b.metadata?.creationTimestamp ?? 0).getTime() -
    new Date(a.lastTimestamp ?? a.metadata?.creationTimestamp ?? 0).getTime()
  )
  const fmtTime = (t?: string) => t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '—'
  const eventsText = events.map((e: any) =>
    `[${e.type}] ${e.reason ?? ''} ×${e.count ?? 1}  ${e.message ?? ''}  @${fmtTime(e.lastTimestamp)}`
  ).join('\n')
  return (
    <Modal title={`PVC 事件 — ${target?.name ?? ''}`} open={open} onCancel={onClose}
      footer={
        <Button icon={<UnorderedListOutlined />} onClick={() => onAiOpen(eventsText)} disabled={events.length === 0}
          style={{ color: '#722ed1', borderColor: '#722ed1' }}>AI 分析</Button>
      } width={900}>
      {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 8 }} />}
      <Table dataSource={events} rowKey={(e: any) => e.metadata?.name ?? Math.random().toString()}
        loading={isLoading} size="small" pagination={false} scroll={{ y: 400 }}
        columns={[
          { title: '类型', dataIndex: 'type', width: 70, render: (v: string) => <Tag color={v === 'Warning' ? 'warning' : 'success'} style={{ margin: 0 }}>{v}</Tag> },
          { title: '原因', dataIndex: 'reason', width: 160, render: (v: string) => <span style={{ fontSize: 12, fontFamily: 'monospace' }}>{v}</span> },
          { title: '次数', dataIndex: 'count', width: 55, render: (v: number) => <span style={{ fontSize: 12, color: v > 1 ? c.warning : c.textSecondary }}>{v ?? 1}</span> },
          { title: '消息', dataIndex: 'message', render: (v: string) => <span style={{ fontSize: 12, wordBreak: 'break-all' }}>{v}</span> },
          { title: '最近时间', width: 160, render: (_: unknown, e: any) => <span style={{ fontSize: 11, color: c.textSecondary }}>{fmtTime(e.lastTimestamp)}</span> },
        ]} />
    </Modal>
  )
}

export default function K8sVolumes() {
  const { data: clusters } = useClusters()
  const { dsId, select } = useSelectedCluster(clusters)

  return (
    <>
      <PageHeader
        title="存储卷"
        extra={<ClusterSelector clusters={clusters ?? []} value={dsId} onChange={select} />}
      />
      <SurfaceCard style={{ margin: '0 24px 24px' }}>
        <VolumesTab dsId={dsId} />
      </SurfaceCard>
    </>
  )
}
