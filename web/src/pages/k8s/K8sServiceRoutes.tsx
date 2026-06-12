/**
 * K8sServiceRoutes – 服务路由（Services + Ingresses），含编辑/删除操作
 */
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Table, Tag, Space, Alert, Typography, Tabs, Input, Button, Popconfirm, Tooltip, message, Select } from 'antd'
import { ReloadOutlined, EditOutlined, DeleteOutlined } from '@ant-design/icons'
import { PageHeader } from '../../components/PageHeader'
import { SurfaceCard } from '../../components/SurfaceCard'
import { useTheme } from '../../hooks/useTheme'
import { http } from '../../api/request'
import { useClusters, useSelectedCluster, byCreation, k8sPagination, fmtCreation, useNamespaces, useK8sList } from './useCluster'
import { ClusterSelector } from './ClusterSelector'
import { YamlEditor } from './YamlEditor'

const { Text } = Typography

interface ServiceItem {
  metadata?: { name?: string; namespace?: string; creationTimestamp?: string }
  spec?: { type?: string; clusterIP?: string; ports?: { port: number; protocol: string; targetPort: unknown }[] }
}

interface IngressItem {
  metadata?: { name?: string; namespace?: string; creationTimestamp?: string }
  spec?: { rules?: { host?: string; http?: { paths?: { path?: string; backend?: unknown }[] } }[] }
}

const serviceTypeColor: Record<string, string> = {
  ClusterIP: 'blue', NodePort: 'orange', LoadBalancer: 'green', ExternalName: 'purple',
}

// ─── ServicesTab ──────────────────────────────────────────────────────────────

function ServicesTab({ dsId }: { dsId: string }) {
  const { c } = useTheme()
  const qc = useQueryClient()
  const [editTarget, setEditTarget] = useState<{ ns: string; name: string } | null>(null)
  const { data: namespaces = [] } = useNamespaces(dsId)
  const [clusterIP, setClusterIP] = useState('')

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
  } = useK8sList<ServiceItem>('/k8s/services', {
    dsId,
    pageSize: 20,
    searchDelay: 300,
    extraParams: clusterIP ? { clusterIP } : undefined,
  })

  const { data: svcDetail, isFetching: svcFetching } = useQuery<unknown>({
    queryKey: ['k8s-service-detail', dsId, editTarget?.ns, editTarget?.name],
    queryFn: () => http.get<unknown>('/k8s/service', {
      params: { ds: dsId, namespace: editTarget!.ns, name: editTarget!.name },
    }),
    enabled: !!editTarget,
    staleTime: 0,
  })

  const updateMut = useMutation({
    mutationFn: ({ ns, name, body }: { ns: string; name: string; body: string }) =>
      http.put(`/k8s/service?ds=${dsId}&namespace=${ns}&name=${name}`, JSON.parse(body)),
    onSuccess: () => { message.success('Service 已更新'); setEditTarget(null); qc.invalidateQueries({ queryKey: ['k8s-services', dsId] }) },
    onError: (e: Error) => message.error(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) =>
      http.delete(`/k8s/service?ds=${dsId}&namespace=${ns}&name=${name}`),
    onSuccess: () => { message.success('Service 已删除'); qc.invalidateQueries({ queryKey: ['k8s-services', dsId] }) },
    onError: (e: Error) => message.error(e.message),
  })

  const sortedItems = (items ?? []).sort(byCreation)

  const columns = [
    {
      title: 'Service 名称',
      render: (_: unknown, s: ServiceItem) => <Text style={{ fontFamily: 'monospace', fontSize: 12 }}>{s.metadata?.name}</Text>,
    },
    { title: '命名空间', width: 140, render: (_: unknown, s: ServiceItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{s.metadata?.namespace}</span> },
    {
      title: '类型', width: 110,
      render: (_: unknown, s: ServiceItem) => {
        const t = s.spec?.type ?? 'ClusterIP'
        return <Tag color={serviceTypeColor[t] ?? 'default'} style={{ margin: 0 }}>{t}</Tag>
      },
    },
    { title: 'ClusterIP', width: 130, render: (_: unknown, s: ServiceItem) => <span style={{ fontSize: 12, fontFamily: 'monospace', color: c.textSecondary }}>{s.spec?.clusterIP}</span> },
    {
      title: '端口', width: 200,
      render: (_: unknown, s: ServiceItem) => (
        <span style={{ fontSize: 11, fontFamily: 'monospace', color: c.textSecondary }}>
          {(s.spec?.ports ?? []).map(p => `${p.port}/${p.protocol}`).join(', ')}
        </span>
      ),
    },
    { title: '创建时间', width: 110, render: (_: unknown, s: ServiceItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{fmtCreation(s.metadata?.creationTimestamp).date}</span> },
    { title: '运行时长', width: 90, render: (_: unknown, s: ServiceItem) => <span style={{ fontSize: 12, color: c.textHint }}>{fmtCreation(s.metadata?.creationTimestamp).age}</span> },
    {
      title: '操作', width: 100, fixed: 'right' as const,
      render: (_: unknown, s: ServiceItem) => (
        <Space size={4}>
          <Tooltip title="编辑 JSON">
            <Button size="small" type="text" icon={<EditOutlined />}
              loading={svcFetching && editTarget?.name === s.metadata?.name}
              onClick={() => setEditTarget({ ns: s.metadata?.namespace ?? '', name: s.metadata?.name ?? '' })} />
          </Tooltip>
          <Popconfirm
            title={`确认删除 Service "${s.metadata?.name}"？`}
            description="此操作不可逆，删除后服务将无法访问"
            okText="删除" cancelText="取消" okButtonProps={{ danger: true }}
            onConfirm={() => deleteMut.mutate({ ns: s.metadata?.namespace ?? '', name: s.metadata?.name ?? '' })}
          >
            <Tooltip title="删除">
              <Button size="small" type="text" danger icon={<DeleteOutlined />} loading={deleteMut.isPending} />
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
          style={{ width: 180 }}
          placeholder="命名空间（全部）"
          allowClear
          showSearch
          value={ns || undefined}
          onChange={v => setNs(v ?? '')}
          options={namespaces.map(n => ({ label: n, value: n }))}
        />
        <Input.Search placeholder="搜索 Service 名称" allowClear style={{ width: 220 }}
          onSearch={doSearch} onChange={e => !e.target.value && doSearch('')} />
        <Input.Search placeholder="搜索 ClusterIP" allowClear style={{ width: 180 }}
          onSearch={v => setClusterIP(v)} onChange={e => !e.target.value && setClusterIP('')} />
        <Button icon={<ReloadOutlined />} onClick={() => refetch()} loading={isLoading}>刷新</Button>
      </Space>
      {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 8 }} />}
      <Table dataSource={sortedItems} columns={columns}
        rowKey={s => `${s.metadata?.namespace}/${s.metadata?.name}`}
        loading={isLoading} size="small" scroll={{ x: 'max-content' }}
        pagination={pagination} />

      <YamlEditor
        title={`编辑 Service: ${editTarget?.name ?? ''}`}
        value={svcDetail}
        open={!!editTarget && !!svcDetail}
        onClose={() => setEditTarget(null)}
        loading={updateMut.isPending}
        onSave={json => updateMut.mutate({ ns: editTarget?.ns ?? '', name: editTarget?.name ?? '', body: json })}
      />
    </>
  )
}

// ─── IngressesTab ──────────────────────────────────────────────────────────────

function IngressesTab({ dsId }: { dsId: string }) {
  const { c } = useTheme()
  const qc = useQueryClient()
  const [editTarget, setEditTarget] = useState<{ ns: string; name: string } | null>(null)
  const { data: namespaces = [] } = useNamespaces(dsId)
  const [hostsSearch, setHostsSearch] = useState('')

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
  } = useK8sList<IngressItem>('/k8s/ingresses', {
    dsId,
    pageSize: 20,
    searchDelay: 300,
    extraParams: hostsSearch ? { hosts: hostsSearch } : undefined,
  })

  const { data: ingDetail, isFetching: ingFetching } = useQuery<unknown>({
    queryKey: ['k8s-ingress-detail', dsId, editTarget?.ns, editTarget?.name],
    queryFn: () => http.get<unknown>('/k8s/ingress', {
      params: { ds: dsId, namespace: editTarget!.ns, name: editTarget!.name },
    }),
    enabled: !!editTarget,
    staleTime: 0,
  })

  const updateMut = useMutation({
    mutationFn: ({ ns, name, body }: { ns: string; name: string; body: string }) =>
      http.put(`/k8s/ingress?ds=${dsId}&namespace=${ns}&name=${name}`, JSON.parse(body)),
    onSuccess: () => { message.success('Ingress 已更新'); setEditTarget(null); qc.invalidateQueries({ queryKey: ['k8s-ingresses', dsId] }) },
    onError: (e: Error) => message.error(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: ({ ns, name }: { ns: string; name: string }) =>
      http.delete(`/k8s/ingress?ds=${dsId}&namespace=${ns}&name=${name}`),
    onSuccess: () => { message.success('Ingress 已删除'); qc.invalidateQueries({ queryKey: ['k8s-ingresses', dsId] }) },
    onError: (e: Error) => message.error(e.message),
  })

  const sortedItems = (items ?? []).sort(byCreation)

  const columns = [
    {
      title: 'Ingress 名称',
      render: (_: unknown, i: IngressItem) => <Text style={{ fontFamily: 'monospace', fontSize: 12 }}>{i.metadata?.name}</Text>,
    },
    { title: '命名空间', width: 140, render: (_: unknown, i: IngressItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{i.metadata?.namespace}</span> },
    {
      title: 'Hosts',
      render: (_: unknown, i: IngressItem) => (
        <span style={{ fontSize: 12, color: c.textSecondary }}>
          {(i.spec?.rules ?? []).map(r => r.host ?? '*').join(', ')}
        </span>
      ),
    },
    {
      title: '路径数', width: 80,
      render: (_: unknown, i: IngressItem) => {
        const cnt = (i.spec?.rules ?? []).reduce((a, r) => a + (r.http?.paths?.length ?? 0), 0)
        return <span style={{ fontSize: 12, color: c.textSecondary }}>{cnt}</span>
      },
    },
    { title: '创建时间', width: 110, render: (_: unknown, i: IngressItem) => <span style={{ fontSize: 12, color: c.textSecondary }}>{fmtCreation(i.metadata?.creationTimestamp).date}</span> },
    { title: '运行时长', width: 90, render: (_: unknown, i: IngressItem) => <span style={{ fontSize: 12, color: c.textHint }}>{fmtCreation(i.metadata?.creationTimestamp).age}</span> },
    {
      title: '操作', width: 100, fixed: 'right' as const,
      render: (_: unknown, i: IngressItem) => (
        <Space size={4}>
          <Tooltip title="编辑 JSON">
            <Button size="small" type="text" icon={<EditOutlined />}
              loading={ingFetching && editTarget?.name === i.metadata?.name}
              onClick={() => setEditTarget({ ns: i.metadata?.namespace ?? '', name: i.metadata?.name ?? '' })} />
          </Tooltip>
          <Popconfirm
            title={`确认删除 Ingress "${i.metadata?.name}"？`}
            description="此操作不可逆"
            okText="删除" cancelText="取消" okButtonProps={{ danger: true }}
            onConfirm={() => deleteMut.mutate({ ns: i.metadata?.namespace ?? '', name: i.metadata?.name ?? '' })}
          >
            <Tooltip title="删除">
              <Button size="small" type="text" danger icon={<DeleteOutlined />} loading={deleteMut.isPending} />
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
          style={{ width: 180 }}
          placeholder="命名空间（全部）"
          allowClear
          showSearch
          value={ns || undefined}
          onChange={v => setNs(v ?? '')}
          options={namespaces.map(n => ({ label: n, value: n }))}
        />
        <Input.Search placeholder="搜索 Ingress 名称" allowClear style={{ width: 220 }}
          onSearch={doSearch} onChange={e => !e.target.value && doSearch('')} />
        <Input.Search placeholder="搜索 Hosts" allowClear style={{ width: 200 }}
          onSearch={v => setHostsSearch(v)} onChange={e => !e.target.value && setHostsSearch('')} />
        <Button icon={<ReloadOutlined />} onClick={() => refetch()} loading={isLoading}>刷新</Button>
      </Space>
      {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 8 }} />}
      <Table dataSource={sortedItems} columns={columns}
        rowKey={i => `${i.metadata?.namespace}/${i.metadata?.name}`}
        loading={isLoading} size="small" scroll={{ x: 'max-content' }}
        pagination={pagination} />

      <YamlEditor
        title={`编辑 Ingress: ${editTarget?.name ?? ''}`}
        value={ingDetail}
        open={!!editTarget && !!ingDetail}
        onClose={() => setEditTarget(null)}
        loading={updateMut.isPending}
        onSave={json => updateMut.mutate({ ns: editTarget?.ns ?? '', name: editTarget?.name ?? '', body: json })}
      />
    </>
  )
}

// ─── K8sServiceRoutes page ────────────────────────────────────────────────────

export default function K8sServiceRoutes() {
  const { data: clusters } = useClusters()
  const { dsId, select } = useSelectedCluster(clusters)

  return (
    <>
      <PageHeader
        title="服务路由"
        extra={<ClusterSelector clusters={clusters ?? []} value={dsId} onChange={select} />}
      />
      <SurfaceCard style={{ margin: '0 24px 24px' }}>
        {!dsId && <Alert type="info" message="请先从上方选择一个集群" style={{ marginBottom: 12 }} />}
        {dsId && (
          <Tabs
            defaultActiveKey="services"
            items={[
              { key: 'services',  label: 'Services',  children: <ServicesTab dsId={dsId} /> },
              { key: 'ingresses', label: 'Ingresses', children: <IngressesTab dsId={dsId} /> },
            ]}
          />
        )}
      </SurfaceCard>
    </>
  )
}
