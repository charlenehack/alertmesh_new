import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Button, DatePicker, Drawer, Empty, Modal, Radio,
  Space, Spin, Table, Tag, Tooltip, Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  DownloadOutlined, FileTextOutlined, PlusOutlined, ReloadOutlined, RobotOutlined,
} from '@ant-design/icons'
import dayjs from 'dayjs'
import type { Dayjs } from 'dayjs'
import { getAIReports, createAIReport } from '../../api/aiReports'
import type { AIReport } from '../../api/aiReports'
import MarkdownView from '../../components/MarkdownView'
import { useThemeMode } from '../../theme/ThemeContext'
import { getColors } from '../../theme/tokens'

const { Title, Text } = Typography
const { RangePicker } = DatePicker

type PeriodType = 'day' | 'week' | 'month'

const PERIOD_LABELS: Record<PeriodType, string> = {
  day: '按天',
  week: '按周',
  month: '按月',
}

// ---- download helper ----
function downloadReport(report: AIReport) {
  const periodLabel = PERIOD_LABELS[report.period as PeriodType] ?? report.period
  const startStr = dayjs(report.start_time).format('YYYY-MM-DD')
  const endStr = dayjs(report.end_time).format('YYYY-MM-DD')
  const filename = `告警报告_${periodLabel}_${startStr}_${endStr}.md`
  const blob = new Blob([report.report], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

const PICKER_MAP: Record<PeriodType, 'date' | 'week' | 'month'> = {
  day: 'date',
  week: 'week',
  month: 'month',
}

const STATUS_MAP: Record<string, { color: string; label: string }> = {
  pending: { color: 'default', label: '等待中' },
  running: { color: 'processing', label: '生成中' },
  done: { color: 'success', label: '已完成' },
  failed: { color: 'error', label: '失败' },
}

const PAGE_SIZE = 20

export default function AIReports() {
  const { mode } = useThemeMode()
  const c = getColors(mode)
  const qc = useQueryClient()

  const [modalOpen, setModalOpen] = useState(false)
  const [period, setPeriod] = useState<PeriodType>('week')
  const [dateRange, setDateRange] = useState<[Dayjs, Dayjs] | null>(null)
  const [drawerReport, setDrawerReport] = useState<AIReport | null>(null)
  const [page, setPage] = useState(1)

  const { data, isLoading, refetch } = useQuery({
    queryKey: ['ai-reports', page],
    queryFn: () => getAIReports((page - 1) * PAGE_SIZE, PAGE_SIZE),
  })

  const reports: AIReport[] = data?.items ?? []
  const total = data?.total ?? 0

  const createMut = useMutation({
    mutationFn: createAIReport,
    onSuccess: () => {
      setModalOpen(false)
      setDateRange(null)
      qc.invalidateQueries({ queryKey: ['ai-reports'] })
    },
  })

  const computeRange = (): { start_time: string; end_time: string } | null => {
    if (!dateRange) return null
    const [start, end] = dateRange
    let s: Dayjs
    let e: Dayjs
    if (period === 'day') {
      s = start.startOf('day')
      e = end.endOf('day')
    } else if (period === 'week') {
      s = start.startOf('week')
      e = end.endOf('week')
    } else {
      s = start.startOf('month')
      e = end.endOf('month')
    }
    return { start_time: s.toISOString(), end_time: e.toISOString() }
  }

  const handleGenerate = () => {
    const range = computeRange()
    if (!range) return
    createMut.mutate({ period, ...range })
  }

  const columns: ColumnsType<AIReport> = [
    {
      title: '周期',
      dataIndex: 'period',
      width: 80,
      render: (p: PeriodType) => (
        <Tag style={{ fontSize: 11 }}>{PERIOD_LABELS[p] ?? p}</Tag>
      ),
    },
    {
      title: '时间范围',
      width: 280,
      render: (_: unknown, row: AIReport) => (
        <span style={{ fontSize: 12, color: c.textSecondary }}>
          {dayjs(row.start_time).format('YYYY-MM-DD HH:mm')}
          {' → '}
          {dayjs(row.end_time).format('YYYY-MM-DD HH:mm')}
        </span>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      width: 90,
      render: (s: string) => {
        const { color, label } = STATUS_MAP[s] ?? { color: 'default', label: s }
        return <Tag color={color} style={{ fontSize: 11 }}>{label}</Tag>
      },
    },
    {
      title: '创建时间',
      dataIndex: 'created_at',
      width: 150,
      render: (t: string) => (
        <Tooltip title={dayjs(t).format('YYYY-MM-DD HH:mm:ss')}>
          <span style={{ fontSize: 12, color: c.textSecondary }}>{dayjs(t).fromNow()}</span>
        </Tooltip>
      ),
    },
    {
      title: '操作',
      width: 130,
      render: (_: unknown, row: AIReport) => (
        <Space size={4}>
          <Button
            size="small"
            type="link"
            icon={<FileTextOutlined />}
            disabled={row.status !== 'done'}
            onClick={() => setDrawerReport(row)}
            style={{ padding: 0 }}
          >
            查看
          </Button>
          <Button
            size="small"
            type="link"
            icon={<DownloadOutlined />}
            disabled={row.status !== 'done'}
            onClick={() => downloadReport(row)}
            style={{ padding: 0, marginLeft: 4 }}
          >
            下载
          </Button>
        </Space>
      ),
    },
  ]

  return (
    <div>
      {/* 页头 */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 20 }}>
        <div>
          <Title level={4} style={{ margin: 0, color: c.textBody, letterSpacing: '-0.01em' }}>
            告警报告
          </Title>
          <span style={{ fontSize: 12, color: c.textTertiary, marginTop: 2, display: 'block' }}>
            由 AI 大模型对指定时间周期内的告警资源进行分析总结
          </span>
        </div>
        <Space>
          <Button
            icon={<ReloadOutlined />}
            onClick={() => { setPage(1); refetch() }}
            style={{ borderColor: c.borderStrong, color: c.textSecondary }}
          >
            刷新
          </Button>
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setModalOpen(true)}
            style={{ background: '#722ed1', borderColor: '#722ed1' }}
          >
            生成报告
          </Button>
        </Space>
      </div>

      {/* 报告列表 */}
      <div style={{
        borderRadius: 10,
        border: `1px solid ${c.border}`,
        overflow: 'hidden',
        background: c.bgSurface,
      }}>
        <Table
          rowKey="id"
          dataSource={reports}
          columns={columns}
          loading={isLoading}
          size="middle"
          locale={{
            emptyText: (
              <Empty
                image={<RobotOutlined style={{ fontSize: 40, color: '#d9d9d9' }} />}
                description="暂无报告，点击「生成报告」开始"
              />
            ),
          }}
          pagination={{
            current: page,
            pageSize: PAGE_SIZE,
            total,
            onChange: (p) => setPage(p),
            showTotal: (t) => `共 ${t} 条`,
            showSizeChanger: false,
            style: { padding: '12px 16px' },
          }}
          style={{ background: 'transparent' }}
        />
      </div>

      {/* 生成报告 Modal */}
      <Modal
        title={
          <Space size={8}>
            <RobotOutlined style={{ color: '#722ed1' }} />
            <span>生成告警报告</span>
          </Space>
        }
        open={modalOpen}
        onCancel={() => { setModalOpen(false); setDateRange(null) }}
        onOk={handleGenerate}
        okText="开始生成"
        okButtonProps={{
          loading: createMut.isPending,
          disabled: !dateRange,
          style: { background: '#722ed1', borderColor: '#722ed1' },
        }}
        width={480}
      >
        <div style={{ padding: '8px 0' }}>
          {createMut.isPending ? (
            <div style={{ textAlign: 'center', padding: '24px 0' }}>
              <Spin tip="AI 正在分析告警数据，请稍候（约 1-3 分钟）..." />
            </div>
          ) : (
            <>
              <div style={{ marginBottom: 16 }}>
                <Text type="secondary" style={{ fontSize: 12 }}>统计周期</Text>
                <div style={{ marginTop: 8 }}>
                  <Radio.Group
                    value={period}
                    onChange={(e) => { setPeriod(e.target.value); setDateRange(null) }}
                    optionType="button"
                    buttonStyle="solid"
                  >
                    <Radio.Button value="day">按天</Radio.Button>
                    <Radio.Button value="week">按周</Radio.Button>
                    <Radio.Button value="month">按月</Radio.Button>
                  </Radio.Group>
                </div>
              </div>
              <div>
                <Text type="secondary" style={{ fontSize: 12 }}>时间范围</Text>
                <div style={{ marginTop: 8 }}>
                  <RangePicker
                    picker={PICKER_MAP[period]}
                    value={dateRange}
                    onChange={(v) => setDateRange(v as [Dayjs, Dayjs] | null)}
                    style={{ width: '100%' }}
                    disabledDate={(d) => d.isAfter(dayjs())}
                  />
                </div>
                <Text type="secondary" style={{ fontSize: 11, marginTop: 4, display: 'block' }}>
                  AI 将分析该时间段内所有告警事件，生成汇总报告
                </Text>
              </div>
              {createMut.isError && (
                <div style={{ marginTop: 12, color: '#f5222d', fontSize: 12 }}>
                  生成失败：{(createMut.error as Error)?.message ?? '未知错误'}
                </div>
              )}
            </>
          )}
        </div>
      </Modal>

      {/* 报告详情 Drawer */}
      <Drawer
        title={
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', paddingRight: 16 }}>
            <Space size={8}>
              <FileTextOutlined style={{ color: '#722ed1' }} />
              <span>
                告警报告
                {drawerReport && (
                  <span style={{ fontSize: 12, color: '#999', marginLeft: 8 }}>
                    {PERIOD_LABELS[drawerReport.period as PeriodType] ?? drawerReport.period}
                    {' · '}
                    {dayjs(drawerReport.start_time).format('YYYY-MM-DD')}
                    {' → '}
                    {dayjs(drawerReport.end_time).format('YYYY-MM-DD')}
                  </span>
                )}
              </span>
            </Space>
            {drawerReport?.status === 'done' && (
              <Button
                size="small"
                icon={<DownloadOutlined />}
                onClick={() => drawerReport && downloadReport(drawerReport)}
              >
                下载 Markdown
              </Button>
            )}
          </div>
        }
        open={!!drawerReport}
        onClose={() => setDrawerReport(null)}
        width={760}
        styles={{ body: { padding: '16px 24px' } }}
      >
        {drawerReport?.report ? (
          <MarkdownView source={drawerReport.report} />
        ) : (
          <Empty description="暂无报告内容" />
        )}
      </Drawer>
    </div>
  )
}
