/**
 * K8sEvents – 全局事件聚合页
 * 相当于 `kubectl get events -A --sort-by=.lastTimestamp`
 * 支持命名空间过滤、类型过滤（Warning/Normal）、关键词搜索、AI 分析
 */
import { useState, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Table, Tag, Space, Alert, Button, Input, Select, Switch, InputNumber, Badge } from 'antd'
import { ReloadOutlined, WarningOutlined } from '@ant-design/icons'
import { PageHeader } from '../../components/PageHeader'
import { SurfaceCard } from '../../components/SurfaceCard'
import { useTheme } from '../../hooks/useTheme'
import { http } from '../../api/request'
import { useClusters, useSelectedCluster, useNamespaces, useAutoRefresh, k8sPagination } from './useCluster'
import { ClusterSelector } from './ClusterSelector'

interface K8sEvent {
  metadata?: { name?: string; namespace?: string; creationTimestamp?: string }
  type?: string
  reason?: string
  message?: string
  count?: number
  firstTimestamp?: string
  lastTimestamp?: string
  involvedObject?: { kind?: string; name?: string; namespace?: string }
  source?: { component?: string; host?: string }
}
interface EventList { items?: K8sEvent[] }

export default function K8sEvents() {
  const { c } = useTheme()
  const { data: clusters } = useClusters()
  const { dsId, select } = useSelectedCluster(clusters)

  const [ns, setNs] = useState('')
  const [typeFilter, setTypeFilter] = useState('')
  const [search, setSearch] = useState('')

  const { data, isLoading, error, refetch } = useQuery<EventList>({
    queryKey: ['k8s-global-events', dsId, ns, typeFilter],
    queryFn: () => http.get<EventList>('/k8s/global/events', {
      params: { ds: dsId, ...(ns ? { namespace: ns } : {}), ...(typeFilter ? { type: typeFilter } : {}) },
    }),
    enabled: !!dsId,
    staleTime: 10_000,
  })

  const { data: namespaces = [] } = useNamespaces(dsId)
  const autoRefresh = useAutoRefresh(() => refetch(), 0)

  const fmtTime = (t?: string) => t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '—'

  const events = useMemo(() => {
    let items = data?.items ?? []
    if (search) {
      const q = search.toLowerCase()
      items = items.filter(e =>
        (e.involvedObject?.name ?? '').toLowerCase().includes(q) ||
        (e.reason ?? '').toLowerCase().includes(q) ||
        (e.message ?? '').toLowerCase().includes(q) ||
        (e.involvedObject?.kind ?? '').toLowerCase().includes(q)
      )
    }
    return items.sort((a, b) =>
      new Date(b.lastTimestamp ?? b.metadata?.creationTimestamp ?? 0).getTime() -
      new Date(a.lastTimestamp ?? a.metadata?.creationTimestamp ?? 0).getTime()
    )
  }, [data, search])

  const warningCount = (data?.items ?? []).filter(e => e.type === 'Warning').length

  const columns = [
    {
      title: '类型', dataIndex: 'type', width: 80,
      render: (v: string) => <Tag color={v === 'Warning' ? 'warning' : 'success'} style={{ margin: 0 }}>{v}</Tag>,
    },
    {
      title: '命名空间', width: 130,
      render: (_: unknown, e: K8sEvent) => (
        <span style={{ fontSize: 12, color: c.textSecondary }}>{e.metadata?.namespace ?? e.involvedObject?.namespace ?? '—'}</span>
      ),
    },
    {
      title: '资源类型', width: 110,
      render: (_: unknown, e: K8sEvent) => (
        <span style={{ fontSize: 12, fontFamily: 'monospace', color: c.textSecondary }}>{e.involvedObject?.kind ?? '—'}</span>
      ),
    },
    {
      title: '资源名称', width: 220,
      render: (_: unknown, e: K8sEvent) => (
        <span style={{ fontSize: 12, fontFamily: 'monospace' }}>{e.involvedObject?.name ?? '—'}</span>
      ),
    },
    {
      title: '原因', dataIndex: 'reason', width: 160,
      render: (v: string) => <span style={{ fontSize: 12, fontFamily: 'monospace' }}>{v}</span>,
    },
    {
      title: '次数', dataIndex: 'count', width: 60,
      render: (v: number) => (
        <span style={{ fontSize: 12, color: v > 5 ? c.danger : v > 1 ? c.warning : c.textSecondary }}>{v ?? 1}</span>
      ),
    },
    {
      title: '消息',
      render: (_: unknown, e: K8sEvent) => (
        <span style={{ fontSize: 12, wordBreak: 'break-word', whiteSpace: 'pre-wrap' }}>{e.message}</span>
      ),
    },
    {
      title: '来源组件', width: 150,
      render: (_: unknown, e: K8sEvent) => (
        <span style={{ fontSize: 11, color: c.textHint, fontFamily: 'monospace' }}>{e.source?.component ?? '—'}</span>
      ),
    },
    {
      title: '最近时间', width: 160,
      render: (_: unknown, e: K8sEvent) => (
        <span style={{ fontSize: 11, color: c.textSecondary }}>{fmtTime(e.lastTimestamp)}</span>
      ),
    },
  ]

  return (
    <>
      <PageHeader
        title={
          <Space>
            <span>集群事件</span>
            {warningCount > 0 && (
              <Badge count={warningCount} color="orange" overflowCount={999}
                style={{ fontSize: 11 }} />
            )}
          </Space>
        }
        extra={
          <Space>
            <ClusterSelector clusters={clusters ?? []} value={dsId} onChange={select} />
            <Button icon={<ReloadOutlined />} onClick={() => refetch()} loading={isLoading}>刷新</Button>
          </Space>
        }
      />
      <SurfaceCard style={{ margin: '0 24px 24px' }}>
        <Space style={{ marginBottom: 12 }} wrap>
          <Select style={{ width: 240 }} placeholder="命名空间（全部）" allowClear showSearch
            value={ns || undefined} onChange={v => setNs(v ?? '')}
            options={namespaces.map(n => ({ label: n, value: n }))} />
          <Select style={{ width: 130 }} placeholder="类型（全部）" allowClear
            value={typeFilter || undefined} onChange={v => setTypeFilter(v ?? '')}
            options={[
              { label: <span style={{ color: '#faad14' }}><WarningOutlined /> Warning</span>, value: 'Warning' },
              { label: 'Normal', value: 'Normal' },
            ]} />
          <Input.Search placeholder="搜索资源名/原因/消息" allowClear style={{ width: 260 }}
            onSearch={setSearch} onChange={e => !e.target.value && setSearch('')} />
          <Space size={4}>
            <Switch size="small" checked={autoRefresh.enabled} onChange={autoRefresh.setEnabled} />
            <span style={{ fontSize: 12 }}>自动刷新</span>
            {autoRefresh.enabled && (
              <InputNumber size="small" min={5} max={3600} value={autoRefresh.interval}
                onChange={v => autoRefresh.setInterval(v ?? 30)} addonAfter="s" style={{ width: 90 }} />
            )}
          </Space>
        </Space>
        {error && <Alert type="error" message={(error as Error).message} style={{ marginBottom: 12 }} />}
        {!dsId && <Alert type="info" message="请先从上方选择一个集群" />}
        {dsId && (
          <Table
            dataSource={events}
            columns={columns}
            rowKey={e => e.metadata?.name ?? Math.random().toString()}
            rowClassName={e => e.type === 'Warning' ? 'event-row-warning' : ''}
            loading={isLoading}
            size="small"
            pagination={k8sPagination}
          />
        )}
      </SurfaceCard>
    </>
  )
}
