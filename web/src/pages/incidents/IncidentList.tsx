import { useState, useEffect } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Table, Tag, Button, Input, Select, Typography, Tooltip
} from 'antd'
import { getColors } from '../../theme/tokens'
import { useThemeMode } from '../../theme/ThemeContext'
import { SearchOutlined, ReloadOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import dayjs from 'dayjs'
import { getIncidents } from '../../api/incidents'
import { useRealtime } from '../../hooks/useRealtime'
import SeverityBadge from '../../components/SeverityBadge'
import StatusBadge from '../../components/StatusBadge'
import type { Incident, Severity, IncidentStatus } from '../../types'

const { Title } = Typography
const { Option } = Select

const PAGE_SIZE = 20

export default function IncidentList() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { mode } = useThemeMode()
  const c = getColors(mode)
  const [page, setPage] = useState(1)
  const [search, setSearch] = useState('')
  const [severityFilter, setSeverityFilter] = useState<string>('')
  const [statusFilter, setStatusFilter] = useState<string>('')
  const [, setTick] = useState(0)

  // 每分钟触发一次重渲染，让 fromNow() 时间文字自动更新
  useEffect(() => {
    const timer = setInterval(() => setTick(t => t + 1), 60_000)
    return () => clearInterval(timer)
  }, [])

  const { data, isLoading } = useQuery({
    queryKey: ['incidents', page],
    queryFn: () => getIncidents((page - 1) * PAGE_SIZE, PAGE_SIZE),
  })

  // Push channel — every incident lifecycle event server-side fires a
  // pg_notify('incident_event') that the realtime hub fans out here.
  // Replaces the 20s refetchInterval that was hammering /api/v1/incidents
  // even when nothing changed.  We invalidate the entire `incidents`
  // query family so all paginated views stay consistent.
  useRealtime(['incidents'], () => {
    qc.invalidateQueries({ queryKey: ['incidents'] })
  })

  const allItems: Incident[] = data?.items ?? []
  const total = data?.total ?? 0

  const filtered = allItems.filter((inc) => {
    const matchSearch = !search || inc.title.toLowerCase().includes(search.toLowerCase())
    const matchSeverity = !severityFilter || inc.severity === severityFilter
    const matchStatus = !statusFilter || inc.status === statusFilter
    return matchSearch && matchSeverity && matchStatus
  })

  const columns = [
    {
      title: '严重度',
      dataIndex: 'severity',
      width: 100,
      render: (s: Severity) => <SeverityBadge severity={s} />,
    },
    {
      title: '事件标题',
      dataIndex: 'title',
      render: (t: string, row: Incident) => (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span
            onClick={() => navigate(`/incidents/${row.id}`)}
            style={{
              fontWeight: 500,
              fontSize: 13,
              color: c.textBody,
              cursor: 'pointer',
              letterSpacing: '0.01em',
              lineHeight: '1.4',
            }}
          >
            {t}
          </span>
          <span style={{ fontSize: 11, color: c.textTertiary }}>
            #{row.id.slice(0, 8)}
          </span>
        </div>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 110,
      render: (s: IncidentStatus) => <StatusBadge status={s} />,
    },
    {
      title: '来源',
      dataIndex: 'source',
      width: 120,
      render: (s: string) => (
        <Tag style={{
          borderRadius: 4,
          fontSize: 11,
          padding: '1px 8px',
          background: c.bgElevated,
          border: `1px solid ${c.border}`,
          color: c.textHint,
        }}>{s}</Tag>
      ),
    },
    {
      title: 'AI',
      dataIndex: 'ai_status',
      width: 80,
      render: (s: string) => {
        const map: Record<string, { color: string; label: string }> = {
          pending: { color: 'default', label: '待分析' },
          running: { color: 'processing', label: '分析中' },
          done: { color: 'success', label: '完成' },
          failed: { color: 'error', label: '失败' },
        }
        const { color, label } = map[s] ?? { color: 'default', label: s }
        return <Tag color={color} style={{ fontSize: 11 }}>{label}</Tag>
      },
    },
    {
      title: '开始时间',
      dataIndex: 'opened_at',
      width: 140,
      render: (t: string) => (
        <Tooltip title={dayjs(t).format('YYYY-MM-DD HH:mm:ss')}>
          <span style={{ fontSize: 12, color: c.textSecondary }}>{dayjs(t).fromNow()}</span>
        </Tooltip>
      ),
    },
  ]

  return (
    <div>
      {/* 页头 */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 20 }}>
        <div>
          <Title level={4} style={{ margin: 0, color: c.textBody, letterSpacing: '-0.01em' }}>
            事件管理
          </Title>
          <span style={{ fontSize: 12, color: c.textTertiary, marginTop: 2, display: 'block' }}>
            共 {total} 条事件
          </span>
        </div>
        <Button
          icon={<ReloadOutlined />}
          onClick={() => {
            setPage(1)
            qc.invalidateQueries({ queryKey: ['incidents'] })
          }}
          style={{ borderColor: c.borderStrong, color: c.textSecondary }}
        >
          刷新
        </Button>
      </div>

      {/* 筛选栏 */}
      <div style={{
        display: 'flex',
        gap: 10,
        marginBottom: 16,
        flexWrap: 'wrap',
        alignItems: 'center',
      }}>
        <Input
          placeholder="搜索事件标题"
          prefix={<SearchOutlined style={{ color: c.textTertiary }} />}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          style={{ width: 240 }}
          allowClear
        />
        <Select
          placeholder="严重度"
          value={severityFilter || undefined}
          onChange={(v) => setSeverityFilter(v ?? '')}
          allowClear
          style={{ width: 110 }}
        >
          {['P0', 'P1', 'P2', 'P3'].map((s) => (
            <Option key={s} value={s}>{s}</Option>
          ))}
        </Select>
        <Select
          placeholder="状态"
          value={statusFilter || undefined}
          onChange={(v) => setStatusFilter(v ?? '')}
          allowClear
          style={{ width: 120 }}
        >
          {[
            { value: 'open', label: '待处理' },
            { value: 'ack', label: '已确认' },
            { value: 'in_progress', label: '处理中' },
            { value: 'resolved', label: '已解决' },
            { value: 'closed', label: '已关闭' },
          ].map((s) => (
            <Option key={s.value} value={s.value}>{s.label}</Option>
          ))}
        </Select>
      </div>

      {/* 表格 */}
      <div style={{
        borderRadius: 10,
        border: `1px solid ${c.border}`,
        overflow: 'hidden',
        background: c.bgSurface,
      }}>
        <Table
          dataSource={filtered}
          columns={columns}
          rowKey="id"
          loading={isLoading}
          size="middle"
          pagination={{
            current: page,
            pageSize: PAGE_SIZE,
            total,
            onChange: (p) => setPage(p),
            showTotal: (t) => `共 ${t} 条`,
            showSizeChanger: false,
            style: { padding: '12px 16px' },
          }}
          onRow={(record) => ({
            onClick: () => navigate(`/incidents/${record.id}`),
            style: { cursor: 'pointer' },
          })}
          style={{ background: 'transparent' }}
        />
      </div>
    </div>
  )
}
