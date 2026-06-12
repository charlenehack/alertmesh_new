/**
 * K8sEndpoints – Endpoint 查看 & 编辑（Tab 组件）
 */
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Table, Tag, Space, Alert, Typography, Input, Button,
  Popconfirm, Tooltip, message, Select, Switch, InputNumber,
} from 'antd'
import {
  ReloadOutlined, EyeOutlined, EditOutlined, DeleteOutlined,
} from '@ant-design/icons'
import { useTheme } from '../../hooks/useTheme'
import { http } from '../../api/request'
import { byCreation, k8sPagination, fmtCreation, useNamespaces, useAutoRefresh, useK8sList } from './useCluster'
import { YamlEditor } from './YamlEditor'

const { Text } = Typography

interface EpSubset {
  addresses?: Array<{ ip?: string; hostname?: string; nodeName?: string; targetRef?: { kind?: string; name?: string; namespace?: string } }>
  notReadyAddresses?: Array<{ ip?: string }>
  ports?: Array<{ name?: string; port?: number; protocol?: string }>
}
interface EpItem {
  metadata?: { name?: string; namespace?: string; creationTimestamp?: string }
  subsets?: EpSubset[]
}

function epEndpoints(ep: EpItem) {
  const subsets = ep.subsets ?? []
  if (subsets.length === 0) return { ready: 0, notReady: 0, ips: [] as string[] }
  let ready = 0, notReady = 0
  const ips: string[] = []
  for (const s of subsets) {
    ready += (s.addresses ?? []).length
    notReady += (s.notReadyAddresses ?? []).length
    for (const a of s.addresses ?? []) ips.push(a.ip ?? '?')
  }
  return { ready, notReady, ips }
}

export function EndpointsTab({ dsId }: { dsId: string }) {
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
  } = useK8sList<EpItem>('/k8s/endpoints', {
    dsId,
    pageSize: 20,
    searchDelay: 300,
  })

  const { data: namespaces = [] } = useNamespaces(dsId)
  const autoRefresh = useAutoRefresh(() => refetch(), 0)

  const { data: epDetail, isFetching: epFetching } = useQuery<unknown>({
    queryKey: ['k8s-endpoint-detail', dsId, editTarget?.ns, editTarget?.name],
    queryFn: () => http.get<unknown>('/k8s/endpoint', {
      params: { ds: dsId, namespace: editTarget!.ns, name: editTarget!.name },
    }),
    enabled: !!editTarget,
    staleTime: 0,
  })

  const updateMut = useMutation({
    mutationFn: ({ ns, name, body }: { ns: string; name: string; body: string }) =>
      http.put(`/k8s/endpoint?ds=${dsId}&namespace=${ns}&name=${name}`, JSON.parse(body)),
    onSuccess: () => { message.success('Endpoint 已更新'); setEditTarget(null); qc.invalidateQueries({ queryKey: ['k8s-endpoints', dsId] }) },
    onError: (e: Error) => message.error(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) =>
      http.delete(`/k8s/endpoint?ds=${dsId}&namespace=${ns}&name=${name}`),
    onSuccess: () => { message.success('Endpoint 已删除'); qc.invalidateQueries({ queryKey: ['k8s-endpoints', dsId] }) },
    onError: (e: Error) => message.error(e.message),
  })

  const sortedItems = (items ?? []).sort(byCreation)

  const columns = [
    { title: 'Endpoint 名称', render: (_: unknown, ep: EpItem) => <Text style={{ fontFamily: 'monospace', fontSize: 12 }}>{ep.metadata?.name}</Text> },
    { title: '命名空间', width: 140, render: (_: unknown, ep: EpItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{ep.metadata?.namespace}</span> },
    {
      title: 'Ready', width: 80,
      render: (_: unknown, ep: EpItem) => {
        const { ready, notReady } = epEndpoints(ep)
        if (ready === 0 && notReady === 0) return <span style={{ color: c.textSecondary }}>—</span>
        return <span style={{ fontSize: 12, color: ready > 0 ? c.success : c.warning }}>{ready}</span>
      },
    },
    {
      title: 'NotReady', width: 80,
      render: (_: unknown, ep: EpItem) => {
        const { notReady } = epEndpoints(ep)
        if (notReady === 0) return <span style={{ color: c.textSecondary }}>0</span>
        return <span style={{ fontSize: 12, color: c.warning }}>{notReady}</span>
      },
    },
    {
      title: 'IP 地址', width: 200,
      render: (_: unknown, ep: EpItem) => {
        const { ips } = epEndpoints(ep)
        if (ips.length === 0) return <span style={{ color: c.textSecondary }}>—</span>
        const display = ips.length > 3 ? [...ips.slice(0, 3), `+${ips.length - 3}`] : ips
        return <span style={{ fontSize: 11, fontFamily: 'monospace', color: c.textSecondary }}>{display.join(', ')}</span>
      },
    },
    {
      title: '端口', width: 200,
      render: (_: unknown, ep: EpItem) => {
        const ports = (ep.subsets ?? []).flatMap(s => (s.ports ?? []).map(p => `${p.port}/${p.protocol ?? 'TCP'}`))
        if (ports.length === 0) return <span style={{ color: c.textSecondary }}>—</span>
        const display = ports.length > 4 ? [...ports.slice(0, 4), `+${ports.length - 4}`] : ports
        return <span style={{ fontSize: 11, fontFamily: 'monospace', color: c.textSecondary }}>{display.join(', ')}</span>
      },
    },
    { title: '创建时间', width: 110, render: (_: unknown, ep: EpItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{fmtCreation(ep.metadata?.creationTimestamp).date}</span> },
    { title: '运行时长', width: 90, render: (_: unknown, ep: EpItem) => <span style={{ fontSize: 12, color: c.textHint }}>{fmtCreation(ep.metadata?.creationTimestamp).age}</span> },
    {
      title: '操作', width: 120, fixed: 'right' as const,
      render: (_: unknown, ep: EpItem) => (
        <Space size={4}>
          <Tooltip title="查看 JSON">
            <Button size="small" type="text" icon={<EyeOutlined />}
              loading={epFetching && editTarget?.name === ep.metadata?.name}
              onClick={() => setEditTarget({ ns: ep.metadata?.namespace ?? '', name: ep.metadata?.name ?? '', readonly: true })} />
          </Tooltip>
          <Tooltip title="编辑 JSON">
            <Button size="small" type="text" icon={<EditOutlined />}
              loading={epFetching && editTarget?.name === ep.metadata?.name}
              onClick={() => setEditTarget({ ns: ep.metadata?.namespace ?? '', name: ep.metadata?.name ?? '' })} />
          </Tooltip>
          <Popconfirm
            title={`确认删除 Endpoint "${ep.metadata?.name}"？`}
            okText="删除" cancelText="取消" okButtonProps={{ danger: true }}
            onConfirm={() => deleteMut.mutate({ ns: ep.metadata?.namespace ?? '', name: ep.metadata?.name ?? '' })}
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
        <Input.Search placeholder="搜索 Endpoint 名称" allowClear style={{ width: 220 }}
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
        rowKey={ep => `${ep.metadata?.namespace}/${ep.metadata?.name}`}
        loading={isLoading} size="small" scroll={{ x: 'max-content' }}
        pagination={pagination} />

      <YamlEditor
        title={`${isReadonly ? '查看' : '编辑'} Endpoint: ${editTarget?.name ?? ''}`}
        value={epDetail} open={!!editTarget && !!epDetail}
        onClose={() => setEditTarget(null)} loading={updateMut.isPending}
        onSave={isReadonly ? undefined : (json => updateMut.mutate({ ns: editTarget?.ns ?? '', name: editTarget?.name ?? '', body: json }))} />
    </>
  )
}