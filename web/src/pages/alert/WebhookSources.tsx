import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Table, Button, Switch, Drawer, Form, Input,
  InputNumber, Space, Popconfirm, App, Typography, Modal, Alert, Tooltip, Tag,
} from 'antd'
import {
  PlusOutlined, EditOutlined, DeleteOutlined, ApiOutlined,
  CopyOutlined, ReloadOutlined, KeyOutlined,
} from '@ant-design/icons'
import {
  getWebhookSources, createWebhookSource, updateWebhookSource,
  rotateWebhookSourceKey, deleteWebhookSource,
} from '../../api/alert'
import { PageHeader } from '../../components/PageHeader'
import { SurfaceCard } from '../../components/SurfaceCard'
import { useTheme } from '../../hooks/useTheme'
import type {
  WebhookPayloadMapping, WebhookSource, WebhookSourceCreated,
} from '../../types'

const { Text, Paragraph } = Typography
const { TextArea } = Input

/** Example for OpenSearch / Elastic alerting → gjson paths (tune to real webhook body). */
const DEFAULT_LOG_ALERT_MAPPING: WebhookPayloadMapping = {
  alertname_path: 'monitor_name',
  severity_path: 'severity',
  fingerprint_path: 'monitor_id',
  summary_path: 'trigger_name',
  description_path: 'error',
  starts_at_path: 'period_start',
  service_path: 'monitor_name',
  label_paths: {
    monitor_id: 'monitor_id',
    trigger_name: 'trigger_name',
  },
}

/**
 * Webhook Sources management page.
 *
 * Each row represents one trusted external alert source that POSTs to
 * `/api/v1/alerts/webhook/{name}` using HTTP Message Signatures (RFC 9421).
 * The Ed25519 PRIVATE key is generated server-side and returned exactly once
 * on create or rotate; we surface it in a modal that warns the operator
 * loudly that closing the modal makes the key unrecoverable.
 */
export default function WebhookSources() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  const { c } = useTheme()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<WebhookSource | null>(null)
  const [form] = Form.useForm()

  const [isUnsigned, setIsUnsigned] = useState(false)

  const [revealed, setRevealed] = useState<WebhookSourceCreated | null>(null)

  const { data, isLoading } = useQuery({
    queryKey: ['webhook-sources'],
    queryFn: getWebhookSources,
  })
  const rows: WebhookSource[] = data ?? []

  type SaveResult =
    | { kind: 'create'; data: WebhookSourceCreated }
    | { kind: 'update'; data: WebhookSource }

  const saveMut = useMutation<SaveResult, Error, Partial<WebhookSource>>({
    mutationFn: async (values) => {
      if (editing) {
        const r = await updateWebhookSource(editing.id, values)
        return { kind: 'update', data: r }
      }
      const r = await createWebhookSource(values)
      return { kind: 'create', data: r }
    },
    onSuccess: (result) => {
      qc.invalidateQueries({ queryKey: ['webhook-sources'] })
      setOpen(false)
      form.resetFields()
      setEditing(null)
      setIsUnsigned(false)
      if (result.kind === 'create') {
        // Only show key modal for signed sources
        if (!result.data.is_unsigned) {
          setRevealed(result.data as WebhookSourceCreated)
        } else {
          message.success('已创建')
        }
      } else {
        message.success('已更新')
      }
    },
  })

  const rotateMut = useMutation({
    mutationFn: rotateWebhookSourceKey,
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ['webhook-sources'] })
      setRevealed(r)
    },
  })

  const deleteMut = useMutation({
    mutationFn: deleteWebhookSource,
    onSuccess: () => {
      message.success('已删除')
      qc.invalidateQueries({ queryKey: ['webhook-sources'] })
    },
  })

  const copy = (text: string, label: string) => {
    navigator.clipboard.writeText(text).then(
      () => message.success(`${label}已复制`),
      () => message.error('复制失败'),
    )
  }

  const columns = [
    {
      title: '名称 (URL path)',
      dataIndex: 'name',
      render: (n: string, row: WebhookSource) => (
        <Space>
          <Text style={{ fontWeight: 500 }}>{n}</Text>
          <Text type="secondary" style={{ fontSize: 11, fontFamily: 'monospace' }}>
            POST /api/v1/alerts/{row.is_unsigned ? 'webhook-plain' : 'webhook'}/{n}
          </Text>
          {row.is_unsigned && <Tag color="blue" style={{ fontSize: 10 }}>无签名</Tag>}
        </Space>
      ),
    },
    {
      title: 'Client ID (keyid)',
      dataIndex: 'client_id',
      width: 220,
      render: (cid: string, row: WebhookSource) => row.is_unsigned
        ? <Text type="secondary" style={{ fontSize: 11 }}>—</Text>
        : (
          <Space size={4}>
            <Text code style={{ fontSize: 11 }}>{cid}</Text>
            <Tooltip title="复制 keyid">
              <Button size="small" type="text" icon={<CopyOutlined />} onClick={() => copy(cid, 'keyid')} />
            </Tooltip>
          </Space>
        ),
    },
    {
      title: '允许时钟偏差',
      dataIndex: 'allow_skew',
      width: 120,
      render: (s: number, row: WebhookSource) => row.is_unsigned
        ? <Text type="secondary" style={{ fontSize: 11 }}>—</Text>
        : <Tag style={{ fontSize: 11 }}>±{s}s</Tag>,
    },
    {
      title: '启用',
      dataIndex: 'is_enabled',
      width: 70,
      render: (v: boolean) => <Switch checked={v} size="small" disabled />,
    },
    {
      title: '最近使用',
      dataIndex: 'last_used_at',
      width: 170,
      render: (t: string | null | undefined) =>
        t ? <Text style={{ fontSize: 11, color: c.textSecondary }}>{new Date(t).toLocaleString()}</Text>
          : <Text style={{ fontSize: 11, color: c.textTertiary }}>—</Text>,
    },
    {
      title: '操作',
      width: 160,
      render: (_: unknown, row: WebhookSource) => (
        <Space>
          <Tooltip title="编辑元数据">
            <Button size="small" icon={<EditOutlined />} type="text"
              onClick={() => {
                setEditing(row)
                setIsUnsigned(row.is_unsigned ?? false)
                form.setFieldsValue({
                  name: row.name,
                  description: row.description,
                  allow_skew: row.allow_skew,
                  is_enabled: row.is_enabled,
                  is_unsigned: row.is_unsigned ?? false,
                  mapping_json: JSON.stringify(row.mapping ?? {}, null, 2),
                })
                setOpen(true)
              }} />
          </Tooltip>
          {!row.is_unsigned && (
            <Popconfirm
              title="轮换密钥？"
              description="旧密钥会立即失效，外部告警源必须使用新返回的私钥重新配置。"
              okText="轮换"
              okButtonProps={{ danger: true, loading: rotateMut.isPending }}
              onConfirm={() => rotateMut.mutate(row.id)}
            >
              <Tooltip title="轮换密钥（旧密钥立即失效）">
                <Button size="small" icon={<ReloadOutlined />} type="text" />
              </Tooltip>
            </Popconfirm>
          )}
          <Popconfirm title="确认删除？" onConfirm={() => deleteMut.mutate(row.id)}>
            <Button size="small" icon={<DeleteOutlined />} type="text" danger />
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <div>
      <PageHeader
        title="Webhook 可信源"
        icon={<ApiOutlined />}
        description="RFC 9421 HTTP Message Signatures · Ed25519"
        extra={
          <Button type="primary" icon={<PlusOutlined />}
            onClick={() => {
              setEditing(null)
              setIsUnsigned(false)
              form.resetFields()
              form.setFieldsValue({
                allow_skew: 300,
                is_enabled: true,
                is_unsigned: false,
                mapping_json: JSON.stringify(DEFAULT_LOG_ALERT_MAPPING, null, 2),
              })
              setOpen(true)
            }}>
            新建
          </Button>
        }
      />

      <SurfaceCard flush>
        <Table dataSource={rows} columns={columns} rowKey="id" loading={isLoading}
          size="small" pagination={{ pageSize: 15 }} />
      </SurfaceCard>

      <Drawer
        title={editing ? '编辑 Webhook 源' : '新建 Webhook 源'}
        open={open}
        size={520}
        onClose={() => { setOpen(false); setEditing(null); setIsUnsigned(false) }}
        styles={{
          header: { background: c.bgSurface, borderBottom: `1px solid ${c.border}`, color: c.textBody },
          body: { background: c.bgSurface, padding: '20px 24px' },
          footer: { background: c.bgSurface, borderTop: `1px solid ${c.border}` },
        }}
        footer={
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
            <Button onClick={() => { setOpen(false); setEditing(null) }}>取消</Button>
            <Button type="primary" loading={saveMut.isPending} onClick={() => form.submit()}>
              {editing ? '保存' : (isUnsigned ? '创建' : '生成密钥并创建')}
            </Button>
          </div>
        }
      >
        {!editing && !isUnsigned && (
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 16 }}
            message="保存后将自动生成 Ed25519 keypair，私钥仅展示一次"
            description="请提前准备好外部告警源（脚本 / 网关）的接入文档；关闭弹窗后无法再次查看私钥，只能通过「轮换密钥」重新生成。"
          />
        )}
        {!editing && isUnsigned && (
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 16 }}
            message="无需签名模式适用于腾讯云/阿里云等不能做签名的云平台"
            description="使用 /api/v1/alerts/webhook-plain/{name} 端点接收告警，无需配置密钥。"
          />
        )}
        <Form
          form={form}
          layout="vertical"
          onFinish={(values) => {
            let mapping: WebhookPayloadMapping
            try {
              mapping = JSON.parse((values.mapping_json as string) || '{}') as WebhookPayloadMapping
            } catch {
              message.error('Payload 映射 不是合法 JSON')
              return
            }
            if (!mapping.alertname_path?.trim() ||
                (!mapping.severity_path?.trim() && !mapping.default_severity?.trim())) {
              message.error('mapping 必须包含 alertname_path 与 (severity_path 或 default_severity)')
              return
            }
            const { mapping_json: _mj, ...rest } = values as Record<string, unknown> & { mapping_json?: string }
            saveMut.mutate({ ...rest, mapping } as Partial<WebhookSource>)
          }}
        >
          <Form.Item
            name="name"
            label="名称（即 URL 中的 {source}）"
            rules={[
              { required: true, message: '名称必填' },
              { pattern: /^[A-Za-z0-9_-]+$/, message: '只能包含字母、数字、下划线和短横线' },
            ]}
            extra={<span style={{ color: c.textTertiary, fontSize: 11 }}>
              例如填写 tencentcloud，对应 URL 为 /api/v1/alerts/{isUnsigned ? 'webhook-plain' : 'webhook'}/tencentcloud
            </span>}
          >
            <Input placeholder="cloudwatch-prod" disabled={!!editing} />
          </Form.Item>
          {!isUnsigned && (
            <Form.Item
              name="allow_skew"
              label="允许时钟偏差（秒）"
              initialValue={300}
              extra={<span style={{ color: c.textTertiary, fontSize: 11 }}>签名 created 参数与服务器时间的最大差値；过大会放宽防重放窗口。</span>}
            >
              <InputNumber style={{ width: '100%' }} min={5} max={3600} />
            </Form.Item>
          )}
          <Form.Item name="is_enabled" label="启用" valuePropName="checked" initialValue={true}>
            <Switch />
          </Form.Item>
          <Form.Item
            name="is_unsigned"
            label="无需签名（用于腾讯云/阿里云等不能签名的平台）"
            valuePropName="checked"
            initialValue={false}
            extra={<span style={{ color: c.textTertiary, fontSize: 11 }}>
              开启后使用 /api/v1/alerts/webhook-plain/ 端点，不生成密钥
            </span>}
          >
            <Switch onChange={(checked) => setIsUnsigned(checked)} />
          </Form.Item>
          <Form.Item name="description" label="描述">
            <Input placeholder="可选描述" />
          </Form.Item>
          <Form.Item
            name="mapping_json"
            label="Payload 映射 (JSON)"
            rules={[{ required: true, message: '请填写 JSON 映射' }]}
            extra={
              <span style={{ color: c.textTertiary, fontSize: 11 }}>
                使用 <Text code>tidwall/gjson</Text> 路径从 Webhook 体取值；新建已预填 OpenSearch 风格示例，请按你方 Monitor 实际字段修改。详见文档 log-alert-denoising.md。
              </span>
            }
          >
            <TextArea
              rows={12}
              style={{ fontFamily: 'monospace', fontSize: 11 }}
              placeholder='{"alertname_path":"monitor_name","severity_path":"severity",...}'
            />
          </Form.Item>
        </Form>
      </Drawer>

      <Modal
        title={
          <Space>
            <KeyOutlined style={{ color: '#fa8c16' }} />
            <span>私钥仅显示一次，请立即保存</span>
          </Space>
        }
        open={!!revealed}
        onCancel={() => setRevealed(null)}
        width={680}
        maskClosable={false}
        keyboard={false}
        footer={[
          <Button key="close" type="primary" onClick={() => setRevealed(null)}>
            我已保存，关闭
          </Button>,
        ]}
      >
        {revealed && (
          <>
            <Alert
              type="warning"
              showIcon
              message="此私钥不会再次显示"
              description="关闭弹窗后服务器无法再吐出此私钥，必须立即拷贝到外部告警源 (CloudWatch / 自研脚本 / SaaS 出口)。如丢失，请使用「轮换密钥」重新生成。"
              style={{ marginBottom: 16 }}
            />

            <Paragraph style={{ marginBottom: 4, color: c.textSecondary, fontSize: 12 }}>
              名称 / URL path
            </Paragraph>
            <Paragraph copyable={{ text: revealed.name, tooltips: ['复制', '已复制'] }}
              style={{ fontFamily: 'monospace', fontSize: 13 }}>
              {revealed.name}
            </Paragraph>

            <Paragraph style={{ marginBottom: 4, color: c.textSecondary, fontSize: 12 }}>
              Client ID (RFC 9421 <code>keyid</code>)
            </Paragraph>
            <Paragraph copyable={{ text: revealed.client_id, tooltips: ['复制', '已复制'] }}
              style={{ fontFamily: 'monospace', fontSize: 13 }}>
              {revealed.client_id}
            </Paragraph>

            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
              <Text style={{ color: c.textSecondary, fontSize: 12 }}>私鑰 (PEM, PKCS#8)</Text>
              <Button size="small" icon={<CopyOutlined />}
                onClick={() => copy(revealed.private_key_pem, '私钥')}>
                复制私钥
              </Button>
            </div>
            <TextArea
              readOnly
              value={revealed.private_key_pem}
              autoSize={{ minRows: 6, maxRows: 12 }}
              style={{ fontFamily: 'monospace', fontSize: 11, marginTop: 6 }}
            />

            <Paragraph style={{ marginTop: 12, color: c.textSecondary, fontSize: 12 }}>
              对应公钥 (PEM, PKIX) - 已存储于服务端
            </Paragraph>
            <TextArea
              readOnly
              value={revealed.public_key}
              autoSize={{ minRows: 3, maxRows: 6 }}
              style={{ fontFamily: 'monospace', fontSize: 11 }}
            />
          </>
        )}
      </Modal>
    </div>
  )
}
