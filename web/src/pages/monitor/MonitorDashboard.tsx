/**
 * MonitorDashboard — 机器资源状态监控看板
 * 从 Prometheus 获取 CPU / 内存 / 磁盘使用率，展示概览环形图 + TOP N 表格
 * 支持可配置的自动刷新间隔
 */
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  Row, Col, Alert, Spin, Typography, Select, Table, Tag, Space, Empty,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  DashboardOutlined, ThunderboltOutlined, HddOutlined, DatabaseOutlined,
} from '@ant-design/icons'
import { PageHeader } from '../../components/PageHeader'
import { SurfaceCard } from '../../components/SurfaceCard'
import { useTheme } from '../../hooks/useTheme'
import { getDataSources, promQuery } from '../../api/datasources'
import type { DataSource, PromQueryResponse } from '../../types'

const { Text } = Typography

// ─── 自动刷新选项 ──────────────────────────────────────────────────────────────
const REFRESH_OPTIONS: { label: string; sec: number }[] = [
  { label: '关闭', sec: 0 },
  { label: '10s', sec: 10 },
  { label: '30s', sec: 30 },
  { label: '1 分钟', sec: 60 },
  { label: '5 分钟', sec: 300 },
]

// ─── 预置 PromQL 查询 ─────────────────────────────────────────────────────────
const QUERIES = {
  // CPU 使用率（按 instance 聚合）
  cpu: '100 - (avg by(instance) (rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)',
  // 内存使用率
  mem: '(1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes) * 100',
  // 磁盘使用率（按 instance + mountpoint，排除 tmpfs 等虚拟文件系统）
  disk: '(1 - node_filesystem_avail_bytes{fstype!~"tmpfs|overlay|shm"} / node_filesystem_size_bytes{fstype!~"tmpfs|overlay|shm"}) * 100',
}

// ─── TOP N 配置 ──────────────────────────────────────────────────────────────
const TOP_N = 10

// ─── 辅助函数 ────────────────────────────────────────────────────────────────

/** 从 Prometheus instant query vector 结果中提取 {instance, value} */
function extractInstances(data: PromQueryResponse): { instance: string; value: number; labels: Record<string, string> }[] {
  if (!data?.data?.result) return []
  const results = data.data.result as Array<{ metric: Record<string, string>; value: [number, string] }>
  return results
    .map((r) => {
      const v = parseFloat(r.value[1])
      // 从 instance (ip:port) 中去掉端口，只保留 IP
      const raw = r.metric.instance || 'unknown'
      const ip = raw.includes(':') ? raw.substring(0, raw.lastIndexOf(':')) : raw
      return {
        instance: ip,
        value: Number.isFinite(v) ? v : 0,
        labels: r.metric,
      }
    })
    .filter((r) => Number.isFinite(r.value))
}

/** 按值降序排列取 TOP N */
function topN(items: { instance: string; value: number; labels: Record<string, string> }[], n: number) {
  return [...items].sort((a, b) => b.value - a.value).slice(0, n)
}

function usageColor(v: number): string {
  if (v >= 85) return '#ff4d4f'
  if (v >= 70) return '#faad14'
  return '#52c41a'
}



// ─── TOP 表格组件 ──────────────────────────────────────────────────────────────

interface TopItem {
  key: number
  rank: number
  instance: string
  value: number
  mountpoint?: string
}

function TopTable({ title, data, loading, icon, extraColumns }: {
  title: string
  data: TopItem[]
  loading: boolean
  icon: React.ReactNode
  extraColumns?: ColumnsType<TopItem>
}) {
  const { isDark } = useTheme()

  const columns: ColumnsType<TopItem> = [
    {
      title: '#',
      dataIndex: 'rank',
      width: 48,
      align: 'center',
      render: (rank: number) => {
        const colors = ['#ff4d4f', '#fa8c16', '#faad14']
        const color = rank <= 3 ? colors[rank - 1] : undefined
        return <Tag color={color ? color : 'default'} style={{ minWidth: 28, textAlign: 'center', fontWeight: rank <= 3 ? 700 : 400 }}>{rank}</Tag>
      },
    },
    {
      title: '主机',
      dataIndex: 'instance',
      ellipsis: true,
      render: (v: string) => <Text style={{ fontSize: 13, fontFamily: 'monospace' }}>{v}</Text>,
    },
    ...(extraColumns ?? []),
    {
      title: '使用率',
      dataIndex: 'value',
      width: 160,
      align: 'right',
      sorter: (a, b) => a.value - b.value,
      defaultSortOrder: 'descend',
      render: (v: number) => {
        const color = usageColor(v)
        return (
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, justifyContent: 'flex-end' }}>
            <div style={{ width: 60, height: 6, borderRadius: 3, background: isDark ? '#2a2a2a' : '#f0f0f0', overflow: 'hidden' }}>
              <div style={{ width: `${Math.min(v, 100)}%`, height: '100%', borderRadius: 3, background: color }} />
            </div>
            <Text strong style={{ color, fontFamily: 'monospace', fontSize: 13, minWidth: 52, textAlign: 'right' }}>
              {v.toFixed(1)}%
            </Text>
          </div>
        )
      },
    },
  ]

  return (
    <SurfaceCard style={{ padding: '12px 16px' }}>
      <div style={{ fontSize: 14, fontWeight: 500, marginBottom: 12, display: 'flex', alignItems: 'center', gap: 6 }}>
        {icon}
        {title}
        <Tag style={{ marginLeft: 'auto', fontSize: 11 }}>TOP {TOP_N}</Tag>
      </div>
      <Table<TopItem>
        size="small"
        rowKey="key"
        columns={columns}
        dataSource={data}
        loading={loading}
        pagination={false}
        locale={{ emptyText: <Empty description="暂无数据" image={Empty.PRESENTED_IMAGE_SIMPLE} /> }}
      />
    </SurfaceCard>
  )
}

// ─── 主组件 ──────────────────────────────────────────────────────────────────

export default function MonitorDashboard() {
  const { isDark } = useTheme()
  const [dsId, setDsId] = useState<string>('')
  const [refreshSec, setRefreshSec] = useState(30)

  // 获取所有 Prometheus 数据源
  const { data: allSources } = useQuery({
    queryKey: ['data-sources'],
    queryFn: () => getDataSources(),
  })
  const promSources = useMemo(
    () => (allSources ?? []).filter((s) => s.kind === 'prometheus' && s.is_enabled),
    [allSources],
  )

  // 自动选择第一个（或默认的）Prometheus 数据源
  const activeDsId = useMemo(() => {
    if (dsId && promSources.some((s) => s.id === dsId)) return dsId
    const def = promSources.find((s) => s.is_default) ?? promSources[0]
    return def?.id ?? ''
  }, [dsId, promSources])

  const enabled = !!activeDsId

  // CPU 使用率查询
  const cpuQ = useQuery({
    queryKey: ['monitor-cpu', activeDsId],
    queryFn: () => promQuery(activeDsId, QUERIES.cpu),
    enabled,
    refetchInterval: refreshSec > 0 ? refreshSec * 1000 : false,
    refetchIntervalInBackground: false,
  })

  // 内存使用率查询
  const memQ = useQuery({
    queryKey: ['monitor-mem', activeDsId],
    queryFn: () => promQuery(activeDsId, QUERIES.mem),
    enabled,
    refetchInterval: refreshSec > 0 ? refreshSec * 1000 : false,
    refetchIntervalInBackground: false,
  })

  // 磁盘使用率查询
  const diskQ = useQuery({
    queryKey: ['monitor-disk', activeDsId],
    queryFn: () => promQuery(activeDsId, QUERIES.disk),
    enabled,
    refetchInterval: refreshSec > 0 ? refreshSec * 1000 : false,
    refetchIntervalInBackground: false,
  })

  const isLoading = cpuQ.isLoading || memQ.isLoading || diskQ.isLoading
  const isFetching = cpuQ.isFetching || memQ.isFetching || diskQ.isFetching
  const hasError = cpuQ.error || memQ.error || diskQ.error

  // 解析数据
  const cpuInstances = useMemo(() => extractInstances(cpuQ.data ?? null as unknown as PromQueryResponse), [cpuQ.data])
  const memInstances = useMemo(() => extractInstances(memQ.data ?? null as unknown as PromQueryResponse), [memQ.data])
  const diskRaw = useMemo(() => {
    if (!diskQ.data?.data?.result) return []
    const results = diskQ.data.data.result as Array<{ metric: Record<string, string>; value: [number, string] }>
    return results
      .map((r) => {
        const v = parseFloat(r.value[1])
        return {
          instance: r.metric.instance || 'unknown',
          value: Number.isFinite(v) ? v : 0,
          labels: r.metric,
          mountpoint: r.metric.mountpoint || '',
        }
      })
      .filter((r) => Number.isFinite(r.value))
  }, [diskQ.data])

  // 去重磁盘（同一 instance 取最大使用率的挂载点）
  const diskInstances = useMemo(() => {
    const map = new Map<string, { instance: string; value: number; labels: Record<string, string>; mountpoint: string }>()
    for (const d of diskRaw) {
      const key = d.instance
      const existing = map.get(key)
      if (!existing || d.value > existing.value) {
        map.set(key, d)
      }
    }
    return Array.from(map.values())
  }, [diskRaw])

  // TOP N 数据
  const cpuTop = useMemo(
    () => topN(cpuInstances, TOP_N).map((item, i) => ({ key: i, rank: i + 1, instance: item.instance, value: item.value })),
    [cpuInstances],
  )
  const memTop = useMemo(
    () => topN(memInstances, TOP_N).map((item, i) => ({ key: i, rank: i + 1, instance: item.instance, value: item.value })),
    [memInstances],
  )
  const diskTop = useMemo(() => {
    const sorted = [...diskRaw].sort((a, b) => b.value - a.value).slice(0, TOP_N)
    return sorted.map((item, i) => ({
      key: i, rank: i + 1, instance: item.instance, value: item.value, mountpoint: item.mountpoint,
    }))
  }, [diskRaw])

  // 高负载统计（>85%）
  const WARN_THRESHOLD = 85
  const cpuWarn = cpuInstances.filter((i) => i.value >= WARN_THRESHOLD).length
  const memWarn = memInstances.filter((i) => i.value >= WARN_THRESHOLD).length
  const diskWarn = diskInstances.filter((i) => i.value >= WARN_THRESHOLD).length

  // 磁盘 TOP 表格的额外列（挂载点）
  const diskExtraColumns: ColumnsType<TopItem> = [
    {
      title: '挂载点',
      dataIndex: 'mountpoint',
      width: 80,
      ellipsis: true,
      render: (v: string) => <Text style={{ fontSize: 11, fontFamily: 'monospace', color: isDark ? '#aaa' : '#666' }}>{v || '-'}</Text>,
    },
  ]

  const anyLoading = isLoading || isFetching

  return (
    <>
      <PageHeader
        title="资源监控"
        icon={<DashboardOutlined />}
        description="基于 Prometheus 的机器资源状态监控"
        extra={
          <Space size={8}>
            <Select
              value={activeDsId}
              onChange={(v) => setDsId(v)}
              style={{ width: 200 }}
              placeholder="选择 Prometheus 数据源"
              options={promSources.map((s) => ({ value: s.id, label: s.name }))}
              notFoundContent={
                <span style={{ fontSize: 12, color: '#999' }}>
                  未找到 Prometheus 数据源，请先在「系统管理 → 数据源」中添加
                </span>
              }
            />
            <Select
              value={refreshSec}
              onChange={(v) => setRefreshSec(Number(v))}
              style={{ width: 120 }}
              options={REFRESH_OPTIONS.map((r) => ({ value: r.sec, label: `刷新：${r.label}` }))}
            />
            {anyLoading && <Spin size="small" />}
          </Space>
        }
      />

      <div style={{ margin: '0 24px 24px' }}>
        {!activeDsId && (
          <Alert
            type="info"
            showIcon
            message="请先在「系统管理 → 数据源」中添加 Prometheus 数据源"
            style={{ marginBottom: 16 }}
          />
        )}

        {hasError && (
          <Alert
            type="error"
            showIcon
            message="查询失败"
            description={String(hasError)}
            style={{ marginBottom: 16 }}
          />
        )}

        {activeDsId && (
          <>
            {/* ── 概览统计 ──────────────────────────── */}
            <Row gutter={[16, 16]} style={{ marginBottom: 20 }}>
              <Col xs={12} sm={6}>
                <SurfaceCard style={{ padding: '16px 20px', textAlign: 'center' }}>
                  <DashboardOutlined style={{ fontSize: 22, color: '#1677ff', marginBottom: 6 }} />
                  <div style={{ fontSize: 28, fontWeight: 700 }}>{cpuInstances.length}</div>
                  <div style={{ fontSize: 12, color: '#999' }}>监控主机</div>
                </SurfaceCard>
              </Col>
              <Col xs={12} sm={6}>
                <SurfaceCard style={{ padding: '16px 20px', textAlign: 'center' }}>
                  <ThunderboltOutlined style={{ fontSize: 22, color: cpuWarn > 0 ? '#ff4d4f' : '#52c41a', marginBottom: 6 }} />
                  <div style={{ fontSize: 28, fontWeight: 700, color: cpuWarn > 0 ? '#ff4d4f' : undefined }}>{cpuWarn}</div>
                  <div style={{ fontSize: 12, color: '#999' }}>CPU 高负载 (&ge;85%)</div>
                </SurfaceCard>
              </Col>
              <Col xs={12} sm={6}>
                <SurfaceCard style={{ padding: '16px 20px', textAlign: 'center' }}>
                  <DatabaseOutlined style={{ fontSize: 22, color: memWarn > 0 ? '#ff4d4f' : '#52c41a', marginBottom: 6 }} />
                  <div style={{ fontSize: 28, fontWeight: 700, color: memWarn > 0 ? '#ff4d4f' : undefined }}>{memWarn}</div>
                  <div style={{ fontSize: 12, color: '#999' }}>内存高负载 (&ge;85%)</div>
                </SurfaceCard>
              </Col>
              <Col xs={12} sm={6}>
                <SurfaceCard style={{ padding: '16px 20px', textAlign: 'center' }}>
                  <HddOutlined style={{ fontSize: 22, color: diskWarn > 0 ? '#ff4d4f' : '#52c41a', marginBottom: 6 }} />
                  <div style={{ fontSize: 28, fontWeight: 700, color: diskWarn > 0 ? '#ff4d4f' : undefined }}>{diskWarn}</div>
                  <div style={{ fontSize: 12, color: '#999' }}>磁盘高负载 (&ge;85%)</div>
                </SurfaceCard>
              </Col>
            </Row>

            {/* ── TOP N 表格 ──────────────────────────── */}
            <Row gutter={[16, 16]}>
              <Col xs={24} lg={8}>
                <TopTable
                  title="CPU 使用率 TOP"
                  data={cpuTop}
                  loading={cpuQ.isLoading}
                  icon={<ThunderboltOutlined style={{ color: '#1677ff' }} />}
                />
              </Col>
              <Col xs={24} lg={8}>
                <TopTable
                  title="内存使用率 TOP"
                  data={memTop}
                  loading={memQ.isLoading}
                  icon={<DatabaseOutlined style={{ color: '#722ed1' }} />}
                />
              </Col>
              <Col xs={24} lg={8}>
                <TopTable
                  title="磁盘使用率 TOP"
                  data={diskTop}
                  loading={diskQ.isLoading}
                  icon={<HddOutlined style={{ color: '#fa8c16' }} />}
                  extraColumns={diskExtraColumns}
                />
              </Col>
            </Row>
          </>
        )}
      </div>
    </>
  )
}
