import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Alert, App, Button, Card, Col, Descriptions, Divider, Empty,
  Popconfirm, Row, Space, Spin, Table, Tabs, Tag, Timeline, Tooltip, Typography,
} from 'antd'
import {
  ArrowLeftOutlined, CheckCircleOutlined, CheckOutlined,
  CloseCircleOutlined, FileTextOutlined, IssuesCloseOutlined,
  NotificationOutlined, RetweetOutlined, RobotOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'
import dayjs from 'dayjs'

import { ackIncident, closeIncident, getAIReport, getIncident, resolveIncident, triggerAI } from '../../../api/incidents'
import { http } from '../../../api/request'
import { useAuthStore } from '../../../store/auth'
import { useRealtime } from '../../../hooks/useRealtime'
import SeverityBadge from '../../../components/SeverityBadge'
import StatusBadge from '../../../components/StatusBadge'
import type { Alert as AlertRow, IncidentStatus, IncidentTimeline, Severity } from '../../../types'

import { AITab } from './AITab'
import { AlertExpandedPanel, KeyValueList } from './KeyValueList'
import {
  AI_STATUS_MAP, currentRepeatRung, repeatRungColor,
  timelineActionMeta, truncateInline,
} from './helpers'
import type { AIReportData } from './types'
import { getColors } from '../../../theme/tokens'
import { useThemeMode } from '../../../theme/ThemeContext'

const { Text, Title } = Typography

export default function IncidentDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { message } = App.useApp()
  const { mode } = useThemeMode()
  const c = getColors(mode)
  const [chatInput, setChatInput] = useState('')
  const [chatLoading, setChatLoading] = useState(false)
  const chatEndRef = useRef<HTMLDivElement>(null)

  const { data: inc, isLoading } = useQuery({
    queryKey: ['incident', id],
    queryFn: () => getIncident(id!),
    enabled: !!id,
  })

  // Per-incident push channel — fired by service.notifyIncidentEvent on
  // ack/resolve/close/append. Replaces the old 15s refetchInterval so the
  // page only hits /api/v1/incidents/:id when something actually changed.
  // We also invalidate the parent list query so navigating back doesn't
  // show a stale row.
  useRealtime(id ? [`incident:${id}`] : [], () => {
    if (!id) return
    qc.invalidateQueries({ queryKey: ['incident', id] })
    qc.invalidateQueries({ queryKey: ['incidents'] })
  })

  // aiDisabled: always false — all incidents can attempt AI analysis.
  // The backend guards ai_enabled per data source; if the source has
  // ai_enabled=false the trigger endpoint returns 400 with a clear message.
  const aiDisabled = false

  const { data: aiData, refetch: refetchAI } = useQuery({
    queryKey: ['ai-report', id],
    queryFn: () => getAIReport(id!),
    enabled: !!id && !aiDisabled,
  })
  const aiReport = aiData as AIReportData | undefined

  const mutOpts = (action: string) => ({
    onSuccess: () => {
      message.success(`${action}成功`)
      qc.invalidateQueries({ queryKey: ['incident', id] })
    },
  })
  const ackMut = useMutation({ mutationFn: () => ackIncident(id!), ...mutOpts('确认') })
  const resolveMut = useMutation({ mutationFn: () => resolveIncident(id!), ...mutOpts('解决') })
  const closeMut = useMutation({ mutationFn: () => closeIncident(id!), ...mutOpts('关闭') })
  const aiMut = useMutation({
    mutationFn: () => triggerAI(id!),
    onSuccess: () => {
      message.success('AI 分析已触发，请稍后查看结果')
      qc.invalidateQueries({ queryKey: ['incident', id] })
    },
  })

  // Real-time AI streaming socket — separate from the realtime hub since
  // the AI agent emits its own progress frames (agent_action, ...).
  const wsRef = useRef<WebSocket | null>(null)
  const [wsMessages, setWsMessages] = useState<Array<{ type: string; content: string }>>([])
  const aiWsToken = useAuthStore((s) => s.token)

  useEffect(() => {
    if (!id || !aiWsToken || aiDisabled) return
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    // /ai/ws is JWT-protected (auth: true on the route, AuthFilter accepts
    // ?token= as a fallback for WebSocket upgrades). Without this query
    // param the upgrade would 401 and the AI tab would lose streaming.
    const url = `${protocol}//${window.location.host}/api/v1/incidents/${id}/ai/ws`
      + `?token=${encodeURIComponent(aiWsToken)}`
    const ws = new WebSocket(url)

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data) as { type?: string; content?: string }
        if (typeof msg.type === 'string' && typeof msg.content === 'string') {
          setWsMessages((prev) => [...prev, { type: msg.type!, content: msg.content! }])
          if (msg.type === 'analysis_done' || msg.type === 'analysis_error') {
            refetchAI()
            qc.invalidateQueries({ queryKey: ['incident', id] })
          }
        }
      } catch {
        // ignore non-JSON messages
      }
    }
    wsRef.current = ws
    return () => { ws.close() }
  }, [id, aiWsToken, aiDisabled, refetchAI, qc])

  useEffect(() => {
    chatEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [aiReport?.conversations, wsMessages])

  // Latest alert in the incident — the overview "最近告警字段" panel surfaces
  // the labels + annotations of the freshest one so operators don't have to
  // flip to the alerts tab. Memoised so the sort doesn't re-run on every
  // unrelated render.
  //
  // Hooks must run unconditionally — handle the loading / 404 fallbacks
  // *after* every hook call.
  const latestAlert = useMemo<AlertRow | undefined>(() => {
    const list = (inc?.alerts ?? []) as AlertRow[]
    if (list.length === 0) return undefined
    return [...list].sort(
      (a, b) => new Date(b.starts_at).getTime() - new Date(a.starts_at).getTime(),
    )[0]
  }, [inc?.alerts])

  const handleChat = async () => {
    if (!chatInput.trim() || !id) return
    const question = chatInput.trim()
    setChatInput('')
    setChatLoading(true)
    try {
      await http.post(`/incidents/${id}/ai/chat`, { message: question })
      refetchAI()
    } catch {
      message.error('AI 对话失败')
    } finally {
      setChatLoading(false)
    }
  }

  if (isLoading) {
    return (
      <div style={{ textAlign: 'center', padding: 80 }}>
        <Spin size="large" />
      </div>
    )
  }
  if (!inc) return <Alert type="error" message="事件不存在" />

  const status = inc.status as IncidentStatus
  const canAck = status === 'open'
  const canResolve = status !== 'resolved' && status !== 'closed'
  const canClose = status === 'resolved'

  const aiInfo = AI_STATUS_MAP[inc.ai_status] ?? { color: '#8c8c8c', text: inc.ai_status }

  const alertColumns = [
    { title: '来源', dataIndex: 'source', width: 110, render: (s: string) => <Tag>{s}</Tag> },
    { title: '状态', dataIndex: 'status', width: 100, render: (s: string) => (
      <Tag color={s === 'firing' ? 'red' : 'green'}>{s === 'firing' ? '触发中' : '已恢复'}</Tag>
    )},
    { title: 'Fingerprint', dataIndex: 'fingerprint', width: 180, ellipsis: true,
      render: (f: string) => <Text code style={{ fontSize: 11 }}>{f.slice(0, 16)}…</Text> },
    { title: '开始时间', dataIndex: 'starts_at', width: 160,
      render: (t: string) => dayjs(t).format('MM-DD HH:mm:ss') },
    { title: '标签', dataIndex: 'labels',
      render: (labels: Record<string, string>) => {
        const entries = Object.entries(labels ?? {})
        const head = entries.slice(0, 6)
        const rest = entries.length - head.length
        return (
          <>
            {head.map(([k, v]) => (
              <Tag key={k} style={{ fontSize: 11, marginBottom: 2 }}>{k}={truncateInline(v, 40)}</Tag>
            ))}
            {rest > 0 && <Tag style={{ fontSize: 11, marginBottom: 2 }}>+{rest}</Tag>}
          </>
        )
      },
    },
    { title: 'annotations', dataIndex: 'annotations', width: 130,
      render: (a: Record<string, string>) => {
        const n = Object.keys(a ?? {}).length
        return n > 0
          ? <Tag color="purple">{n} 项</Tag>
          : <Text type="secondary">—</Text>
      },
    },
  ]

  return (
    <div>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 20 }}>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/incidents')} type="text" />
        <div style={{ flex: 1 }}>
          <Title level={4} style={{ margin: 0, color: c.textBody }}>{inc.title}</Title>
          <Space size={8} style={{ marginTop: 4 }} wrap>
            <SeverityBadge severity={inc.severity as Severity} />
            <StatusBadge status={status} />
            <Tag>{inc.source}</Tag>
            {inc.parent_incident_id && (
              <Tag color="purple" icon={<RetweetOutlined />}>
                <Link to={`/incidents/${inc.parent_incident_id}`}>
                  延续自 #{inc.parent_incident_id.slice(0, 8)}
                </Link>
              </Tag>
            )}
            <Tag icon={<NotificationOutlined />} color={repeatRungColor(currentRepeatRung(inc.opened_at))}>
              当前阶梯：{currentRepeatRung(inc.opened_at).label}
            </Tag>
            {(inc.notification_count ?? 0) > 0 && (
              <Tooltip title={inc.last_notified_at ? `最近一次通知：${dayjs(inc.last_notified_at).format('YYYY-MM-DD HH:mm:ss')}` : ''}>
                <Tag color="blue">已通知 {inc.notification_count} 次</Tag>
              </Tooltip>
            )}
            {inc.auto_resolved_at && (
              <Tag color="green" icon={<IssuesCloseOutlined />}>自动恢复</Tag>
            )}
          </Space>
        </div>

        <Space>
          {canAck && (
            <Popconfirm title="确认该事件？" onConfirm={() => ackMut.mutate()}>
              <Button icon={<CheckOutlined />} loading={ackMut.isPending}>确认</Button>
            </Popconfirm>
          )}
          {canResolve && (
            <Popconfirm title="标记为已解决？" onConfirm={() => resolveMut.mutate()}>
              <Button type="primary" icon={<CheckCircleOutlined />} loading={resolveMut.isPending}>解决</Button>
            </Popconfirm>
          )}
          {canClose && (
            <Popconfirm title="关闭该事件？" onConfirm={() => closeMut.mutate()}>
              <Button danger icon={<CloseCircleOutlined />} loading={closeMut.isPending}>关闭</Button>
            </Popconfirm>
          )}
          {aiDisabled ? (
            <Tooltip title="该事件的告警源未启用 AI 分析（请前往数据源配置开启 ai_enabled）">
              <Tag style={{ padding: '4px 10px', fontSize: 12 }}>
                <RobotOutlined style={{ marginRight: 4 }} />
                AI 分析未启用
              </Tag>
            </Tooltip>
          ) : (
            <Button
              icon={<RobotOutlined />}
              loading={aiMut.isPending}
              onClick={() => aiMut.mutate()}
              disabled={inc.ai_status === 'running'}
              style={{ borderColor: '#722ed1', color: '#722ed1' }}
            >
              {inc.ai_status === 'running' ? 'AI 分析中…' : '触发 AI 分析'}
            </Button>
          )}
        </Space>
      </div>

      <Tabs
        defaultActiveKey="overview"
        items={[
          {
            key: 'overview',
            label: '概览',
            children: (
              <OverviewTab inc={inc} aiInfo={aiInfo} latestAlert={latestAlert} />
            ),
          },
          {
            key: 'alerts',
            label: `告警列表 (${inc.alerts?.length ?? 0})`,
            children: (
              <Card style={{ borderRadius: 12, border: 'none', boxShadow: '0 2px 12px rgba(0,0,0,0.06)' }}>
                {inc.alerts && inc.alerts.length > 0 ? (
                  <Table<AlertRow>
                    dataSource={inc.alerts as AlertRow[]}
                    columns={alertColumns}
                    rowKey="id"
                    size="small"
                    pagination={{ pageSize: 10 }}
                    expandable={{
                      expandedRowRender: (r) => <AlertExpandedPanel alert={r} />,
                      rowExpandable: (r) =>
                        Object.keys(r.labels ?? {}).length > 0 ||
                        Object.keys(r.annotations ?? {}).length > 0 ||
                        !!r.raw_payload,
                    }}
                  />
                ) : (
                  <Empty description="暂无关联告警" />
                )}
              </Card>
            ),
          },
          {
            key: 'timeline',
            label: '时间线',
            children: (
              <Card style={{ borderRadius: 12, border: 'none', boxShadow: '0 2px 12px rgba(0,0,0,0.06)' }}>
                {inc.timeline && inc.timeline.length > 0 ? (
                  <Timeline
                    mode="left"
                    items={inc.timeline.map((t: IncidentTimeline) => {
                      const meta = timelineActionMeta(t.action)
                      return {
                        label: dayjs(t.created_at).format('MM-DD HH:mm:ss'),
                        color: meta.color,
                        dot: meta.icon,
                        children: (
                          <div>
                            <Text strong>{meta.label}</Text>
                            {t.username && (
                              <Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>by {t.username}</Text>
                            )}
                            {t.message && (
                              <div style={{ marginTop: 4, fontSize: 12, color: '#8c8c8c' }}>{t.message}</div>
                            )}
                          </div>
                        ),
                      }
                    })}
                  />
                ) : (
                  <Empty description="暂无时间线记录" />
                )}
              </Card>
            ),
          },
          // AI tab is hidden entirely when the incident's data source is
          // not on the AI whitelist (see service.shouldRunAI). Use a
          // null + filter pattern instead of conditional `&&` because
          // antd Tabs choke on falsy items.
          ...(aiDisabled ? [] : [{
            key: 'ai',
            label: <span><RobotOutlined style={{ marginRight: 4 }} />AI 分析</span>,
            children: (
              <AITab
                inc={inc}
                aiReport={aiReport}
                aiInfo={aiInfo}
                wsMessages={wsMessages}
                chatInput={chatInput}
                setChatInput={setChatInput}
                chatLoading={chatLoading}
                chatEndRef={chatEndRef}
                handleChat={handleChat}
                onTrigger={() => aiMut.mutate()}
                triggerLoading={aiMut.isPending}
              />
            ),
          }]),
        ]}
      />
    </div>
  )
}

// ─── OverviewTab ─────────────────────────────────────────────────────────────
//
// Kept inline because it shares the parent's `inc` heavily and only renders
// in one place; promoting to a separate file would mean threading 8+ props.

interface OverviewTabProps {
  inc: NonNullable<Awaited<ReturnType<typeof getIncident>>>
  aiInfo: { color: string; text: string }
  latestAlert: AlertRow | undefined
}

function OverviewTab({ inc, aiInfo, latestAlert }: OverviewTabProps) {
  const { mode } = useThemeMode()
  const c = getColors(mode)
  return (
    <div>
      <Card
        style={{ borderRadius: 12, border: `1px solid ${c.border}`, boxShadow: 'none', marginBottom: 16 }}
      >
        <Descriptions column={2} size="small">
          <Descriptions.Item label="事件 ID"><Text code>{inc.id}</Text></Descriptions.Item>
          <Descriptions.Item label="分组 Key"><Text code ellipsis>{inc.group_key}</Text></Descriptions.Item>
          <Descriptions.Item label="开始时间">
            {dayjs(inc.opened_at).format('YYYY-MM-DD HH:mm:ss')}
          </Descriptions.Item>
          <Descriptions.Item label="最近告警时间">
            {inc.last_alert_at ? (
              <Tooltip title={dayjs(inc.last_alert_at).format('YYYY-MM-DD HH:mm:ss')}>
                {dayjs(inc.last_alert_at).format('MM-DD HH:mm:ss')}
              </Tooltip>
            ) : '—'}
          </Descriptions.Item>
          <Descriptions.Item label="确认时间">
            {inc.acked_at ? dayjs(inc.acked_at).format('YYYY-MM-DD HH:mm:ss') : '—'}
          </Descriptions.Item>
          <Descriptions.Item label="解决时间">
            {inc.resolved_at ? dayjs(inc.resolved_at).format('YYYY-MM-DD HH:mm:ss') : '—'}
            {inc.auto_resolved_at && (
              <Tag color="green" style={{ marginLeft: 8 }}>自动</Tag>
            )}
          </Descriptions.Item>
          <Descriptions.Item label="累计通知次数">{inc.notification_count ?? 0}</Descriptions.Item>
          <Descriptions.Item label="最近一次通知">
            {inc.last_notified_at ? dayjs(inc.last_notified_at).format('YYYY-MM-DD HH:mm:ss') : '—'}
          </Descriptions.Item>
          {inc.parent_incident_id && (
            <Descriptions.Item label="延续自" span={2}>
              <Link to={`/incidents/${inc.parent_incident_id}`}>
                <Text code>{inc.parent_incident_id}</Text>
              </Link>
              <Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
                上一次同 group_key 事件已超过 reopen 窗口，新建独立事件
              </Text>
            </Descriptions.Item>
          )}
          <Descriptions.Item label="AI 状态">
            <span style={{ color: aiInfo.color, fontWeight: 600 }}>
              <ThunderboltOutlined /> {aiInfo.text}
            </span>
          </Descriptions.Item>
        </Descriptions>

        {inc.labels && Object.keys(inc.labels).length > 0 && (
          <>
            <Divider style={{ margin: '12px 0' }} />
            <div>
              <Text type="secondary" style={{ fontSize: 12 }}>标签</Text>
              <div style={{ marginTop: 6 }}>
                {Object.entries(inc.labels).map(([k, v]) => (
                  <Tag key={k} style={{ marginBottom: 4 }}>{k}={v}</Tag>
                ))}
              </div>
            </div>
          </>
        )}
      </Card>

      {/* 最近告警字段 — incident.labels only carries the group_key
          dimensions (typically source/severity/alertname); the full
          Kafka-mapped labels and *all* annotations live on each
          alerts[*] row. We surface the latest one here so operators
          don't have to flip to the 告警列表 tab to see what payload
          actually triggered the page. */}
      {latestAlert && (
        <Card
          title={
            <Space size={8} style={{ fontSize: 14 }}>
              <FileTextOutlined style={{ color: '#1677ff' }} />
              <span style={{ fontWeight: 600 }}>最近告警字段</span>
              <Text type="secondary" style={{ fontSize: 12, fontWeight: 400 }}>
                {dayjs(latestAlert.starts_at).format('MM-DD HH:mm:ss')} · fp{' '}
                <Text code style={{ fontSize: 11 }}>{latestAlert.fingerprint.slice(0, 16)}…</Text>
              </Text>
            </Space>
          }
          size="small"
          style={{ borderRadius: 12, border: 'none', boxShadow: '0 2px 12px rgba(0,0,0,0.06)' }}
          styles={{ body: { padding: '12px 16px' } }}
        >
          <Row gutter={16}>
            <Col xs={24} lg={12}>
              <Text type="secondary" style={{ fontSize: 12 }}>labels</Text>
              <div style={{ marginTop: 6 }}>
                <KeyValueList data={latestAlert.labels} emptyText="无 labels" />
              </div>
            </Col>
            <Col xs={24} lg={12}>
              <Text type="secondary" style={{ fontSize: 12 }}>annotations</Text>
              <div style={{ marginTop: 6 }}>
                <KeyValueList data={latestAlert.annotations} emptyText="无 annotations" />
              </div>
            </Col>
          </Row>
        </Card>
      )}
    </div>
  )
}
