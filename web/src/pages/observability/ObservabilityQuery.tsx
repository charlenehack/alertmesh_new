import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Card, Input, Button, Select, Space, Typography, Alert, Tag,
  Spin, Empty, Table, Modal, Form, Row, Col, message,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  PlayCircleOutlined, SaveOutlined, DeleteOutlined,
  RobotOutlined, HistoryOutlined, BulbOutlined,
} from '@ant-design/icons'
import { getDataSources } from '../../api/datasources'
import type { DataSource } from '../../types'
import {
  generateObservabilityQuery,
  executeObservabilityQuery,
  summarizeObservabilityResult,
  getSavedQueries,
  createSavedQuery,
  deleteSavedQuery,
} from '../../api/observability'
import type { SavedQuery, SavedQueryWritePayload } from '../../api/observability'
import { useTheme } from '../../hooks/useTheme'
import { PageHeader } from '../../components/PageHeader'

const { Text } = Typography
const { TextArea } = Input
const { Option } = Select

type Kind = 'prometheus' | 'opensearch' | 'elastic'

interface ExecuteForm {
  data_source_kind: Kind
  data_source_id?: string
  natural_language: string
  query_text: string
}

const KIND_LABEL: Record<Kind, string> = {
  prometheus: 'Prometheus',
  opensearch: 'OpenSearch',
  elastic: 'Elasticsearch',
}

const RANGES = [
  { label: '过去 5 分钟', sec: 5 * 60 },
  { label: '过去 10 分钟', sec: 10 * 60 },
  { label: '过去 30 分钟', sec: 30 * 60 },
  { label: '过去 1 小时', sec: 60 * 60 },
]

export default function ObservabilityQuery() {
  const { isDark } = useTheme()
  const [searchParams, setSearchParams] = useSearchParams()
  const queryClient = useQueryClient()

  const [form, setForm] = useState<ExecuteForm>({
    data_source_kind: (searchParams.get('kind') as Kind) || 'prometheus',
    data_source_id: searchParams.get('id') || undefined,
    natural_language: '',
    query_text: '',
  })
  const [result, setResult] = useState<unknown>(null)
  const [resultError, setResultError] = useState<string>('')
  const [summary, setSummary] = useState<string>('')
  const [saveOpen, setSaveOpen] = useState(false)
  const [saveForm] = Form.useForm()

  const { data: dataSources } = useQuery({
    queryKey: ['dataSources'],
    queryFn: () => getDataSources(),
  })

  const { data: savedQueries, isLoading: savedLoading } = useQuery({
    queryKey: ['savedQueries', form.data_source_kind],
    queryFn: () => getSavedQueries(form.data_source_kind),
  })

  const filteredSources = useMemo(
    () => dataSources?.filter((d) => d.kind === form.data_source_kind) || [],
    [dataSources, form.data_source_kind]
  )

  useEffect(() => {
    if (!form.data_source_id && filteredSources.length > 0 && form.data_source_kind !== 'prometheus') {
      setForm((f) => ({ ...f, data_source_id: filteredSources[0].id }))
    }
  }, [filteredSources, form.data_source_id, form.data_source_kind])

  const generateMutation = useMutation({
    mutationFn: generateObservabilityQuery,
    onSuccess: (res) => {
      setForm((f) => ({ ...f, query_text: res.query_text }))
    },
    onError: (err: Error) => {
      message.error('生成失败: ' + err.message)
    },
  })

  const executeMutation = useMutation({
    mutationFn: executeObservabilityQuery,
    onSuccess: (res) => {
      setResult(res)
      setResultError('')
      setSummary('')
    },
    onError: (err: Error) => {
      setResult(null)
      setResultError(err.message || '执行失败')
      setSummary('')
    },
  })

  const summarizeMutation = useMutation({
    mutationFn: summarizeObservabilityResult,
    onSuccess: (res) => setSummary(res.summary),
    onError: (err: Error) => message.error('摘要失败: ' + err.message),
  })

  const saveMutation = useMutation({
    mutationFn: createSavedQuery,
    onSuccess: () => {
      message.success('保存成功')
      setSaveOpen(false)
      saveForm.resetFields()
      queryClient.invalidateQueries({ queryKey: ['savedQueries'] })
    },
    onError: (err: Error) => message.error('保存失败: ' + err.message),
  })

  const deleteMutation = useMutation({
    mutationFn: deleteSavedQuery,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['savedQueries'] }),
    onError: (err: Error) => message.error('删除失败: ' + err.message),
  })

  const handleGenerate = () => {
    if (!form.natural_language.trim()) {
      message.warning('请输入想查询的内容')
      return
    }
    generateMutation.mutate({
      data_source_kind: form.data_source_kind,
      natural_language: form.natural_language,
    })
  }

  const handleExecute = () => {
    if (!form.query_text.trim()) {
      message.warning('查询语句为空')
      return
    }
    const now = Math.floor(Date.now() / 1000)
    const start = now - 10 * 60
    executeMutation.mutate({
      data_source_kind: form.data_source_kind,
      data_source_id: form.data_source_id,
      query_text: form.query_text,
      start_time: String(start),
      end_time: String(now),
      step: '15s',
    })
  }

  const handleSave = (values: { name: string; is_shared: boolean }) => {
    const payload: SavedQueryWritePayload = {
      name: values.name,
      data_source_kind: form.data_source_kind,
      data_source_id: form.data_source_id,
      natural_language: form.natural_language,
      query_text: form.query_text,
      is_shared: values.is_shared,
    }
    saveMutation.mutate(payload)
  }

  const loadSaved = (q: SavedQuery) => {
    setForm({
      data_source_kind: q.data_source_kind,
      data_source_id: q.data_source_id,
      natural_language: q.natural_language,
      query_text: q.query_text,
    })
    setResult(null)
    setResultError('')
  }

  const onKindChange = (kind: Kind) => {
    setSearchParams({ kind })
    setForm({
      data_source_kind: kind,
      data_source_id: undefined,
      natural_language: '',
      query_text: '',
    })
    setResult(null)
    setResultError('')
  }

  const columns = usePrometheusResultColumns(result)
  const esColumns = useOpenSearchResultColumns(result)

  return (
    <div>
      <PageHeader
        title="可观测性查询助手"
        icon={<RobotOutlined />}
        description="用自然语言生成 Prometheus / OpenSearch 查询，确认后再执行"
      />

      <Row gutter={[16, 16]} style={{ padding: '0 24px 24px' }}>
        <Col xs={24} lg={16}>
          <Card title="查询" style={{ background: isDark ? '#1f1f1f' : '#fff' }}>
            <Space direction="vertical" style={{ width: '100%' }} size="middle">
              <Space wrap>
                <Select
                  value={form.data_source_kind}
                  onChange={onKindChange}
                  style={{ width: 140 }}
                >
                  <Option value="prometheus">Prometheus</Option>
                  <Option value="opensearch">OpenSearch</Option>
                  <Option value="elastic">Elasticsearch</Option>
                </Select>

                {form.data_source_kind !== 'prometheus' && (
                  <Select
                    placeholder="选择数据源"
                    value={form.data_source_id}
                    onChange={(v) => setForm((f) => ({ ...f, data_source_id: v }))}
                    style={{ minWidth: 200 }}
                    allowClear
                  >
                    {filteredSources.map((ds) => (
                      <Option key={ds.id} value={ds.id}>{ds.name}</Option>
                    ))}
                  </Select>
                )}

                {form.data_source_kind === 'prometheus' && (
                  <Select
                    placeholder="选择数据源（可选，默认用全局 Prometheus）"
                    value={form.data_source_id}
                    onChange={(v) => setForm((f) => ({ ...f, data_source_id: v }))}
                    style={{ minWidth: 260 }}
                    allowClear
                  >
                    {filteredSources.map((ds) => (
                      <Option key={ds.id} value={ds.id}>{ds.name}</Option>
                    ))}
                  </Select>
                )}
              </Space>

              <TextArea
                rows={3}
                placeholder="例如：近10分钟 nginx 状态码为非200的请求数；当前磁盘占用率最高的TOP10机器"
                value={form.natural_language}
                onChange={(e) => setForm((f) => ({ ...f, natural_language: e.target.value }))}
              />

              <Button
                type="primary"
                icon={<RobotOutlined />}
                loading={generateMutation.isPending}
                onClick={handleGenerate}
              >
                AI 生成查询
              </Button>

              <TextArea
                rows={4}
                placeholder={form.data_source_kind === 'prometheus' ? 'PromQL 查询语句' : 'OpenSearch DSL JSON'}
                value={form.query_text}
                onChange={(e) => setForm((f) => ({ ...f, query_text: e.target.value }))}
                style={{ fontFamily: 'monospace' }}
              />

              <Space>
                <Button
                  type="primary"
                  icon={<PlayCircleOutlined />}
                  loading={executeMutation.isPending}
                  onClick={handleExecute}
                  disabled={!form.query_text.trim()}
                >
                  执行查询
                </Button>
                <Button
                  icon={<SaveOutlined />}
                  onClick={() => setSaveOpen(true)}
                  disabled={!form.query_text.trim()}
                >
                  保存查询
                </Button>
                <Button
                  icon={<BulbOutlined />}
                  loading={summarizeMutation.isPending}
                  onClick={() => {
                    if (!result) return
                    summarizeMutation.mutate({
                      data_source_kind: form.data_source_kind,
                      natural_language: form.natural_language || form.query_text,
                      result,
                    })
                  }}
                  disabled={!result}
                >
                  AI 智能摘要
                </Button>
              </Space>

              {resultError && (
                <Alert type="error" message={resultError} showIcon />
              )}

              {summary && (
                <Alert
                  type="info"
                  showIcon
                  icon={<BulbOutlined />}
                  message="AI 摘要"
                  description={summary}
                />
              )}

              {result && form.data_source_kind === 'prometheus' && (
                <PrometheusResult data={result as PromResponse} columns={columns} />
              )}

              {result && form.data_source_kind !== 'prometheus' && (
                <OpenSearchResult data={result as ESResponse} columns={esColumns} />
              )}
            </Space>
          </Card>
        </Col>

        <Col xs={24} lg={8}>
          <Card title={<><HistoryOutlined /> 已保存查询</>} style={{ background: isDark ? '#1f1f1f' : '#fff' }}>
            {savedLoading ? <Spin /> : (
              <Space direction="vertical" style={{ width: '100%' }}>
                {savedQueries?.length === 0 && <Empty description="暂无保存的查询" image={Empty.PRESENTED_IMAGE_SIMPLE} />}
                {savedQueries?.map((q) => (
                  <Card
                    key={q.id}
                    size="small"
                    hoverable
                    onClick={() => loadSaved(q)}
                    bodyStyle={{ padding: 12 }}
                  >
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                      <div>
                        <Text strong>{q.name}</Text>
                        <div>
                          <Tag>{KIND_LABEL[q.data_source_kind]}</Tag>
                          {q.is_shared && <Tag color="blue">共享</Tag>}
                        </div>
                        <Text type="secondary" style={{ fontSize: 12 }} ellipsis>{q.natural_language}</Text>
                      </div>
                      <Button
                        type="text"
                        danger
                        size="small"
                        icon={<DeleteOutlined />}
                        onClick={(e) => {
                          e.stopPropagation()
                          deleteMutation.mutate(q.id)
                        }}
                        loading={deleteMutation.isPending}
                      />
                    </div>
                  </Card>
                ))}
              </Space>
            )}
          </Card>
        </Col>
      </Row>

      <Modal
        open={saveOpen}
        title="保存查询"
        onCancel={() => setSaveOpen(false)}
        onOk={() => saveForm.submit()}
        confirmLoading={saveMutation.isPending}
      >
        <Form form={saveForm} onFinish={handleSave} layout="vertical">
          <Form.Item
            name="name"
            label="名称"
            rules={[{ required: true, message: '请输入名称' }]}
          >
            <Input placeholder="例如：nginx 非 200 统计" />
          </Form.Item>
          <Form.Item name="is_shared" valuePropName="checked" initialValue={false}>
            {/* eslint-disable-next-line jsx-a11y/label-has-associated-control */}
            <label style={{ cursor: 'pointer' }}>
              <Input type="checkbox" style={{ marginRight: 8 }} />
              共享给所有人
            </label>
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}

// ─── Prometheus result rendering ─────────────────────────────────────────────

interface PromResponse {
  status?: string
  data?: {
    resultType?: string
    result?: Array<{
      metric: Record<string, string>
      value?: [number, string]
      values?: Array<[number, string]>
    }>
  }
}

function usePrometheusResultColumns(data: unknown): ColumnsType<Record<string, string | number>> {
  return useMemo(() => {
    if (!data) return []
    const prom = data as PromResponse
    const result = prom.data?.result || []
    if (result.length === 0) return []

    const keys = new Set<string>(['__value__'])
    result.forEach((r) => Object.keys(r.metric).forEach((k) => keys.add(k)))

    const cols: ColumnsType<Record<string, string | number>> = Array.from(keys).map((k) => ({
      title: k,
      dataIndex: k,
      key: k,
      ellipsis: true,
    }))
    return cols
  }, [data])
}

function PrometheusResult({ data, columns }: { data: PromResponse; columns: ColumnsType<Record<string, string | number>> }) {
  const rows = useMemo(() => {
    const result = data.data?.result || []
    return result.map((r, i) => {
      const latest = r.value ? r.value[1] : (r.values && r.values.length ? r.values[r.values.length - 1][1] : '')
      return { key: i, ...r.metric, __value__: latest }
    })
  }, [data])

  if (data.status !== 'success') {
    return <Alert type="warning" message="Prometheus 返回非成功状态" description={JSON.stringify(data)} />
  }
  if (rows.length === 0) {
    return <Empty description="无数据" />
  }
  return <Table columns={columns} dataSource={rows} size="small" scroll={{ x: 'max-content' }} />
}

// ─── OpenSearch result rendering ─────────────────────────────────────────────

interface ESResponse {
  hits?: {
    total?: number | { value: number }
    hits?: Array<{
      _index?: string
      _id?: string
      _source?: Record<string, unknown>
    }>
  }
}

function useOpenSearchResultColumns(data: unknown): ColumnsType<Record<string, unknown>> {
  return useMemo(() => {
    if (!data) return []
    const es = data as ESResponse
    const hits = es.hits?.hits || []
    if (hits.length === 0) return []

    const keys = new Set<string>()
    hits.forEach((h) => {
      if (h._source) Object.keys(h._source).forEach((k) => keys.add(k))
    })

    return Array.from(keys).map((k) => ({
      title: k,
      dataIndex: k,
      key: k,
      ellipsis: true,
      render: (v: unknown) => {
        if (typeof v === 'object' && v !== null) return <Text code>{JSON.stringify(v).slice(0, 200)}</Text>
        return String(v ?? '')
      },
    }))
  }, [data])
}

function OpenSearchResult({ data, columns }: { data: ESResponse; columns: ColumnsType<Record<string, unknown>> }) {
  const rows = useMemo(() => {
    return (data.hits?.hits || []).map((h, i) => ({ key: i, ...(h._source || {}) }))
  }, [data])

  const total = typeof data.hits?.total === 'number' ? data.hits.total : (data.hits?.total as { value?: number })?.value

  return (
    <div>
      <Text type="secondary">命中: {total ?? '-'} 条</Text>
      {rows.length === 0 ? <Empty description="无日志" /> : (
        <Table columns={columns} dataSource={rows} size="small" scroll={{ x: 'max-content' }} />
      )}
    </div>
  )
}
