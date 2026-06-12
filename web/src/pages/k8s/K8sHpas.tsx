/**
 * K8sHpas – HPA 查看 & 编辑（Tab 组件）
 */
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Table, Tag, Space, Alert, Typography, Input, Button,
  Popconfirm, Tooltip, message, Select, Switch, InputNumber,
} from 'antd'
import {
  ReloadOutlined, EditOutlined, DeleteOutlined, EyeOutlined,
} from '@ant-design/icons'
import { useTheme } from '../../hooks/useTheme'
import { http } from '../../api/request'
import { byCreation, k8sPagination, fmtCreation, useNamespaces, useAutoRefresh, useK8sList } from './useCluster'
import { YamlEditor } from './YamlEditor'

const { Text } = Typography

interface HpaSpec {
  scaleTargetRef?: { kind?: string; name?: string; apiVersion?: string }
  minReplicas?: number
  maxReplicas?: number
  metrics?: Array<{
    type?: string
    resource?: { name?: string; target?: { type?: string; averageUtilization?: number; averageValue?: string } }
    pods?: { metric?: { name?: string }; target?: { type?: string; averageValue?: string } }
    external?: { metric?: { name?: string; selector?: unknown }; target?: { type?: string; averageValue?: string; value?: string } }
  }>
}

interface HpaStatus {
  currentReplicas?: number
  desiredReplicas?: number
  currentMetrics?: unknown[]
  conditions?: Array<{ type?: string; status?: string }>
}

interface HpaItem {
  metadata?: { name?: string; namespace?: string; creationTimestamp?: string }
  spec?: HpaSpec
  status?: HpaStatus
}

function hpaStatusTag(status?: HpaStatus) {
  const scaling = (status?.conditions ?? []).find(c => c.type === 'ScalingActive')
  if (!scaling) return <Tag style={{ margin: 0 }}>未知</Tag>
  if (scaling.status === 'True') return <Tag color="success" style={{ margin: 0 }}>正常</Tag>
  if (scaling.status === 'False') return <Tag color="error" style={{ margin: 0 }}>异常</Tag>
  return <Tag color="warning" style={{ margin: 0 }}>未知</Tag>
}

function metricDesc(spec?: HpaSpec) {
  const metrics = spec?.metrics ?? []
  if (metrics.length === 0) return '—'
  return metrics.map(m => {
    if (m.resource) {
      const util = m.resource.target?.averageUtilization
      const avg = m.resource.target?.averageValue
      const target = util ? `${util}%` : avg ?? '?'
      return `${m.resource.name}(${target})`
    }
    if (m.pods) return `Pods/${m.pods.metric?.name ?? '?'}`
    if (m.external) return `External/${m.external.metric?.name ?? '?'}`
    return m.type ?? '?'
  }).join(', ')
}

export function HpasTab({ dsId }: { dsId: string }) {
  const { c } = useTheme()
  const qc = useQueryClient()
  const [editTarget, setEditTarget] = useState<{ ns: string; name: string; readonly?: boolean } | null>(null)

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
  } = useK8sList<HpaItem>('/k8s/hpas', {
    dsId,
    pageSize: 20,
    searchDelay: 300,
  })

  const { data: namespaces = [] } = useNamespaces(dsId)
  const autoRefresh = useAutoRefresh(() => refetch(), 0)

  const { data: hpaDetail, isFetching: hpaFetching } = useQuery<unknown>({
    queryKey: ['k8s-hpa-detail', dsId, editTarget?.ns, editTarget?.name],
    queryFn: () => http.get<unknown>('/k8s/hpa', {
      params: { ds: dsId, namespace: editTarget!.ns, name: editTarget!.name },
    }),
    enabled: !!editTarget,
    staleTime: 0,
  })

  const updateMut = useMutation({
    mutationFn: ({ ns, name, body }: { ns: string; name: string; body: string }) =>
      http.put(`/k8s/hpa?ds=${dsId}&namespace=${ns}&name=${name}`, JSON.parse(body)),
    onSuccess: () => { message.success('HPA 已更新'); setEditTarget(null); qc.invalidateQueries({ queryKey: ['k8s-hpas', dsId] }) },
    onError: (e: Error) => message.error(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) =>
      http.delete(`/k8s/hpa?ds=${dsId}&namespace=${ns}&name=${name}`),
    onSuccess: () => { message.success('HPA 已删除'); qc.invalidateQueries({ queryKey: ['k8s-hpas', dsId] }) },
    onError: (e: Error) => message.error(e.message),
  })

  const sortedItems = (items ?? []).sort(byCreation)

  const columns = [
    { title: 'HPA 名称', render: (_: unknown, h: HpaItem) => <Text style={{ fontFamily: 'monospace', fontSize: 12 }}>{h.metadata?.name}</Text> },
    { title: '命名空间', width: 140, render: (_: unknown, h: HpaItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{h.metadata?.namespace}</span> },
    { title: '目标', width: 200, render: (_: unknown, h: HpaItem) => {
      const ref = h.spec?.scaleTargetRef
      if (!ref?.name) return <span style={{ color: c.textSecondary }}>—</span>
      return <span style={{ fontSize: 12, color: c.textSecondary }}>{ref.kind}/{ref.name}</span>
    }},
    { title: '副本', width: 130, render: (_: unknown, h: HpaItem) => {
      const cur = h.status?.currentReplicas ?? 0
      const min = h.spec?.minReplicas ?? 0
      const max = h.spec?.maxReplicas ?? 0
      return <span style={{ fontSize: 12 }}><span style={{ color: c.textStrong }}>{cur}</span><span style={{ color: c.textSecondary }}> / {min}-{max}</span></span>
    }},
    { title: '指标', width: 220, render: (_: unknown, h: HpaItem) => <span style={{ fontSize: 11, color: c.textSecondary, fontFamily: 'monospace' }}>{metricDesc(h.spec)}</span> },
    { title: '状态', width: 80, render: (_: unknown, h: HpaItem) => hpaStatusTag(h.status) },
    { title: '创建时间', width: 110, render: (_: unknown, h: HpaItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{fmtCreation(h.metadata?.creationTimestamp).date}</span> },
    { title: '运行时长', width: 90, render: (_: unknown, h: HpaItem) => <span style={{ fontSize: 12, color: c.textHint }}>{fmtCreation(h.metadata?.creationTimestamp).age}</span> },
    {
      title: '操作', width: 120, fixed: 'right' as const,
      render: (_: unknown, h: HpaItem) => (
        <Space size={4}>
          <Tooltip title="查看 JSON">
            <Button size="small" type="text" icon={<EyeOutlined />}
              loading={hpaFetching && editTarget?.name === h.metadata?.name}
              onClick={() => setEditTarget({ ns: h.metadata?.namespace ?? '', name: h.metadata?.name ?? '', readonly: true })} />
          </Tooltip>
          <Tooltip title="编辑 JSON">
            <Button size="small" type="text" icon={<EditOutlined />}
              loading={hpaFetching && editTarget?.name === h.metadata?.name}
              onClick={() => setEditTarget({ ns: h.metadata?.namespace ?? '', name: h.metadata?.name ?? '' })} />
          </Tooltip>
          <Popconfirm
            title={`确认删除 HPA "${h.metadata?.name}"？`} description="删除后目标工作负载将不再自动扩缩容"
            okText="删除" cancelText="取消" okButtonProps={{ danger: true }}
            onConfirm={() => deleteMut.mutate({ ns: h.metadata?.namespace ?? '', name: h.metadata?.name ?? '' })}
          >
            <Tooltip title="删除">
              <Button size="small" type="text" danger icon={<DeleteOutlined />} loading={deleteMut.isPending} />
            </Tooltip>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const isReadonly = editTarget?.readonly === true

  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <Select style={{ width: 240 }} placeholder="命名空间（全部）" allowClear showSearch
          value={ns || undefined} onChange={v => setNs(v ?? '')}
          options={namespaces.map(n => ({ label: n, value: n }))} />
        <Input.Search placeholder="搜索 HPA 名称" allowClear style={{ width: 220 }}
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
        rowKey={h => `${h.metadata?.namespace}/${h.metadata?.name}`}
        loading={isLoading} size="small" scroll={{ x: 'max-content' }}
        pagination={pagination} />

      <YamlEditor
        title={`${isReadonly ? '查看' : '编辑'} HPA: ${editTarget?.name ?? ''}`}
        value={hpaDetail} open={!!editTarget && !!hpaDetail}
        onClose={() => setEditTarget(null)} loading={updateMut.isPending}
        onSave={isReadonly ? undefined : (json => updateMut.mutate({ ns: editTarget?.ns ?? '', name: editTarget?.name ?? '', body: json }))} />
    </>
  )
}