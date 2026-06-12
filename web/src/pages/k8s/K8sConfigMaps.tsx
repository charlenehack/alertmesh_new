/**
 * K8sConfigMaps – ConfigMap 查看 & 编辑（Tab 组件）
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
import { ConfigMapEditor } from './ConfigMapEditor'

const { Text } = Typography

interface CmItem {
  metadata?: {
    name?: string
    namespace?: string
    creationTimestamp?: string
  }
  data?: Record<string, string>
  binaryData?: Record<string, string>
}

export function ConfigMapsTab({ dsId }: { dsId: string }) {
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
  } = useK8sList<CmItem>('/k8s/configmaps', {
    dsId,
    pageSize: 20,
    searchDelay: 300,
  })

  const { data: namespaces = [] } = useNamespaces(dsId)
  const autoRefresh = useAutoRefresh(() => refetch(), 0)

  const { data: cmDetail, isFetching: cmFetching } = useQuery<unknown>({
    queryKey: ['k8s-configmap-detail', dsId, editTarget?.ns, editTarget?.name],
    queryFn: () => http.get<unknown>('/k8s/configmap', {
      params: { ds: dsId, namespace: editTarget!.ns, name: editTarget!.name },
    }),
    enabled: !!editTarget,
    staleTime: 0,
  })

  const updateMut = useMutation({
    mutationFn: ({ ns, name, body }: { ns: string; name: string; body: string }) =>
      http.put(`/k8s/configmap?ds=${dsId}&namespace=${ns}&name=${name}`, JSON.parse(body)),
    onSuccess: () => {
      message.success('ConfigMap 已更新')
      setEditTarget(null)
      qc.invalidateQueries({ queryKey: ['k8s-configmaps', dsId] })
    },
    onError: (e: Error) => message.error(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) =>
      http.delete(`/k8s/configmap?ds=${dsId}&namespace=${ns}&name=${name}`),
    onSuccess: () => {
      message.success('ConfigMap 已删除')
      qc.invalidateQueries({ queryKey: ['k8s-configmaps', dsId] })
    },
    onError: (e: Error) => message.error(e.message),
  })

  const sortedItems = (items ?? []).sort(byCreation)

  const columns = [
    {
      title: 'ConfigMap 名称',
      render: (_: unknown, cm: CmItem) => (
        <Text style={{ fontFamily: 'monospace', fontSize: 12 }}>{cm.metadata?.name}</Text>
      ),
    },
    {
      title: '命名空间', width: 150,
      render: (_: unknown, cm: CmItem) => (
        <span style={{ fontSize: 12, color: c.textSecondary }}>{cm.metadata?.namespace}</span>
      ),
    },
    {
      title: 'Data 键数', width: 100,
      render: (_: unknown, cm: CmItem) => (
        <Tag color="blue" style={{ margin: 0 }}>{Object.keys(cm.data ?? {}).length}</Tag>
      ),
    },
    {
      title: 'BinaryData 键数', width: 120,
      render: (_: unknown, cm: CmItem) => {
        const n = Object.keys(cm.binaryData ?? {}).length
        return n > 0 ? <Tag color="purple" style={{ margin: 0 }}>{n}</Tag> : <span style={{ color: c.textSecondary }}>—</span>
      },
    },
    {
      title: 'Data 键列表', width: 280,
      render: (_: unknown, cm: CmItem) => {
        const keys = Object.keys(cm.data ?? {})
        if (keys.length === 0) return <span style={{ color: c.textSecondary }}>—</span>
        const display = keys.length > 8 ? [...keys.slice(0, 8), `+${keys.length - 8}…`] : keys
        return (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 2 }}>
            {display.map(k => (
              <span key={k} style={{ fontSize: 11, fontFamily: 'monospace', color: c.textSecondary,
                background: 'rgba(0,0,0,0.04)', borderRadius: 3, padding: '0 4px' }}>
                {k}
              </span>
            ))}
          </div>
        )
      },
    },
    { title: '创建时间', width: 110, render: (_: unknown, cm: CmItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{fmtCreation(cm.metadata?.creationTimestamp).date}</span> },
    { title: '运行时长', width: 90, render: (_: unknown, cm: CmItem) => <span style={{ fontSize: 12, color: c.textHint }}>{fmtCreation(cm.metadata?.creationTimestamp).age}</span> },
    {
      title: '操作', width: 120, fixed: 'right' as const,
      render: (_: unknown, cm: CmItem) => (
        <Space size={4}>
          <Tooltip title="查看 JSON">
            <Button size="small" type="text" icon={<EyeOutlined />}
              loading={cmFetching && editTarget?.name === cm.metadata?.name}
              onClick={() => setEditTarget({ ns: cm.metadata?.namespace ?? '', name: cm.metadata?.name ?? '', readonly: true })} />
          </Tooltip>
          <Tooltip title="编辑 JSON">
            <Button size="small" type="text" icon={<EditOutlined />}
              loading={cmFetching && editTarget?.name === cm.metadata?.name}
              onClick={() => setEditTarget({ ns: cm.metadata?.namespace ?? '', name: cm.metadata?.name ?? '' })} />
          </Tooltip>
          <Popconfirm
            title={`确认删除 ConfigMap "${cm.metadata?.name}"？`}
            description="此操作不可逆，使用该 ConfigMap 的 Pod 可能会受影响"
            okText="删除" cancelText="取消" okButtonProps={{ danger: true }}
            onConfirm={() => deleteMut.mutate({ ns: cm.metadata?.namespace ?? '', name: cm.metadata?.name ?? '' })}
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
        <Input.Search
          placeholder="搜索 ConfigMap 名称" allowClear style={{ width: 220 }}
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
      <Table
        dataSource={sortedItems}
        columns={columns}
        rowKey={cm => `${cm.metadata?.namespace}/${cm.metadata?.name}`}
        loading={isLoading}
        size="small"
        scroll={{ x: 'max-content' }}
        pagination={pagination}
      />

      <ConfigMapEditor
        title={`${isReadonly ? '查看' : '编辑'} ConfigMap: ${editTarget?.name ?? ''}`}
        value={cmDetail}
        open={!!editTarget && !!cmDetail}
        onClose={() => setEditTarget(null)}
        loading={updateMut.isPending}
        onSave={isReadonly ? undefined : (json => updateMut.mutate({
          ns: editTarget?.ns ?? '',
          name: editTarget?.name ?? '',
          body: json,
        }))}
      />
    </>
  )
}