/**
 * K8sResourceQuotas – Namespace 级别资源配额查看
 * 相当于 `kubectl get resourcequotas -A`
 */
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Table, Tag, Space, Alert, Button, Input, Select, Switch, InputNumber } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { PageHeader } from '../../components/PageHeader'
import { SurfaceCard } from '../../components/SurfaceCard'
import { useTheme } from '../../hooks/useTheme'
import { http } from '../../api/request'
import { useClusters, useSelectedCluster, useNamespaces, useAutoRefresh, k8sPagination } from './useCluster'
import { ClusterSelector } from './ClusterSelector'

interface ResourceQuota {
  metadata?: { name?: string; namespace?: string }
  spec?: {
    hard?: Record<string, string>
  }
  status?: {
    hard?: Record<string, string>
    used?: Record<string, string>
  }
}
interface QuotaList { items?: ResourceQuota[] }

// 格式化资源值 (cpu: cores→m, memory: Gi/Mi)
function fmtRes(name: string, value: string): string {
  if (!value) return '—'
  const num = parseFloat(value)
  if (isNaN(num)) return value

  switch (name) {
    case 'cpu':
    case 'limits.cpu':
    case 'requests.cpu':
      return num < 1 ? `${Math.round(num * 1000)}m` : `${num}C`
    case 'memory':
    case 'limits.memory':
    case 'requests.memory':
      if (num >= 1073741824) return `${(num / 1073741824).toFixed(1)}Gi`
      if (num >= 1048576) return `${(num / 1048576).toFixed(0)}Mi`
      return `${num}`
    default:
      return String(num)
  }
}

export default function K8sResourceQuotas() {
  const { c } = useTheme()
  const { data: clusters } = useClusters()
  const { dsId, select } = useSelectedCluster(clusters)
  const [ns, setNs] = useState('')

  const { data, isLoading, error, refetch } = useQuery<QuotaList>({
    queryKey: ['k8s-resourcequotas', dsId, ns],
    queryFn: () => http.get<QuotaList>('/k8s/resourcequotas', {
      params: { ds: dsId, ...(ns ? { namespace: ns } : {}) },
    }),
    enabled: !!dsId,
    staleTime: 10_000,
  })

  const { data: namespaces = [] } = useNamespaces(dsId)
  const autoRefresh = useAutoRefresh(() => refetch(), 0)

  const columns = [
    { title: '配额名称', dataIndex: ['metadata', 'name'], width: 180,
      render: (v: string) => <span style={{ fontSize: 12, fontFamily: 'monospace' }}>{v}</span> },
    { title: '命名空间', dataIndex: ['metadata', 'namespace'], width: 130,
      render: (v: string) => <span style={{ fontSize: 12, color: c.textSecondary }}>{v}</span> },
    {
      title: 'CPU (requests/limits)', width: 200,
      render: (_: unknown, q: ResourceQuota) => {
        const hard = q.status?.hard ?? q.spec?.hard ?? {}
        const used = q.status?.used ?? {}
        const reqH = hard['requests.cpu'], reqU = used['requests.cpu']
        const limH = hard['limits.cpu'], limU = used['limits.cpu']
        if (!reqH && !limH) return <span style={{ fontSize: 11, color: c.textHint }}>—</span>
        const reqPct = reqH ? ((parseFloat(reqU ?? '0') / parseFloat(reqH)) * 100).toFixed(0) : ''
        const limPct = limH ? ((parseFloat(limU ?? '0') / parseFloat(limH)) * 100).toFixed(0) : ''
        return (
          <span style={{ fontSize: 11, fontFamily: 'monospace' }}>
            req: {fmtRes('cpu', reqU ?? '0')}/{fmtRes('cpu', reqH)} ({reqPct}%)
            {limH && ` | lim: ${fmtRes('cpu', limU ?? '0')}/${fmtRes('cpu', limH)} (${limPct}%)`}
          </span>
        )
      },
    },
    {
      title: '内存 (requests/limits)', width: 220,
      render: (_: unknown, q: ResourceQuota) => {
        const hard = q.status?.hard ?? q.spec?.hard ?? {}
        const used = q.status?.used ?? {}
        const reqH = hard['requests.memory'], reqU = used['requests.memory']
        const limH = hard['limits.memory'], limU = used['limits.memory']
        if (!reqH && !limH) return <span style={{ fontSize: 11, color: c.textHint }}>—</span>
        const reqPct = reqH ? ((parseFloat(reqU ?? '0') / parseFloat(reqH)) * 100).toFixed(0) : ''
        const limPct = limH ? ((parseFloat(limU ?? '0') / parseFloat(limH)) * 100).toFixed(0) : ''
        return (
          <span style={{ fontSize: 11, fontFamily: 'monospace' }}>
            req: {fmtRes('memory', reqU ?? '0')}/{fmtRes('memory', reqH)} ({reqPct}%)
            {limH && ` | lim: ${fmtRes('memory', limU ?? '0')}/${fmtRes('memory', limH)} (${limPct}%)`}
          </span>
        )
      },
    },
    {
      title: '其他', width: 280,
      render: (_: unknown, q: ResourceQuota) => {
        const hard = q.status?.hard ?? q.spec?.hard ?? {}
        const used = q.status?.used ?? {}
        const skip = new Set(['requests.cpu', 'limits.cpu', 'requests.memory', 'limits.memory', 'cpu', 'memory'])
        const items: string[] = []
        for (const [k, v] of Object.entries(hard)) {
          if (skip.has(k)) continue
          const u = used[k] ?? '0'
          const pct = v ? ((parseFloat(u) / parseFloat(v)) * 100).toFixed(0) : '?'
          items.push(`${k}: ${u}/${v} (${pct}%)`)
        }
        if (items.length === 0) return <span style={{ fontSize: 11, color: c.textHint }}>—</span>
        return items.map((item, i) => (
          <span key={i} style={{ fontSize: 10, fontFamily: 'monospace', color: c.textSecondary, marginRight: 12 }}>{item}</span>
        ))
      },
    },
  ]

  return (
    <>
      <PageHeader
        title="资源配额"
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
            dataSource={data?.items ?? []}
            columns={columns}
            rowKey={q => `${q.metadata?.namespace}/${q.metadata?.name}`}
            loading={isLoading}
            size="small"
            pagination={k8sPagination}
          />
        )}
      </SurfaceCard>
    </>
  )
}
