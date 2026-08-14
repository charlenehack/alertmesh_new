import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Table, Button, Switch, Drawer, Form, Input, Select, InputNumber,
  Space, Popconfirm, App, Typography, Tag, Tooltip, Alert,
} from 'antd'
import {
  PlusOutlined, EditOutlined, DeleteOutlined, RobotOutlined,
  ThunderboltOutlined, CheckCircleOutlined, ExperimentOutlined,
} from '@ant-design/icons'
import {
  getLLMProviders, createLLMProvider, updateLLMProvider,
  deleteLLMProvider, setDefaultLLMProvider, testLLMProvider,
} from '../../api/ai'
import { encryptSecret } from '../../api/crypto'
import { PageHeader } from '../../components/PageHeader'
import { SurfaceCard } from '../../components/SurfaceCard'
import { useTheme } from '../../hooks/useTheme'
import type { LLMProvider } from '../../types'

const { Text } = Typography
const { Option } = Select

// The placeholder the backend uses to mask api_key in list responses.
// Sending this value back on update tells the server to keep the existing
// ciphertext untouched.
const API_KEY_MASK = '******'

// Curated list of provider kinds.  `provider` ultimately drives which SDK
// the backend agent uses; for OpenAI-compatible endpoints (DeepSeek, Ollama,
// vLLM, …) keep "openai" and just point base_url to the right host.
const PROVIDER_OPTIONS = [
  { value: 'openai', label: 'OpenAI / OpenAI-Compatible (DeepSeek, vLLM, Ollama, …)' },
  { value: 'azure', label: 'Azure OpenAI' },
  { value: 'ollama', label: 'Ollama (native)' },
  { value: 'anthropic', label: 'Anthropic Claude' },
  { value: 'qoder', label: 'Qoder Cloud Agents' },
]

/**
 * LLM Providers management page (admin-only).
 *
 * Lets administrators wire up the LLM backend used by the AI agent without
 * shelling into the database.  The api_key is AES-256-GCM encrypted at rest;
 * list/get responses always return "******" so the raw secret never lands
 * in browser state.  Editing a row keeps the existing ciphertext when the
 * field is left blank or as the placeholder.
 */
export default function LLMProviders() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  const { c } = useTheme()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<LLMProvider | null>(null)
  const [form] = Form.useForm()
  const [testing, setTesting] = useState<string | null>(null)
  const provider = Form.useWatch('provider', form)

  const { data, isLoading } = useQuery({
    queryKey: ['llm-providers'],
    queryFn: getLLMProviders,
  })
  const rows: LLMProvider[] = data ?? []

  const saveMut = useMutation({
    mutationFn: async (values: Partial<LLMProvider>) => {
      if (editing) return updateLLMProvider(editing.id, values)
      return createLLMProvider(values)
    },
    onSuccess: () => {
      message.success(editing ? '已更新' : '已创建')
      qc.invalidateQueries({ queryKey: ['llm-providers'] })
      setOpen(false)
      setEditing(null)
      form.resetFields()
    },
  })

  const deleteMut = useMutation({
    mutationFn: deleteLLMProvider,
    onSuccess: () => {
      message.success('已删除')
      qc.invalidateQueries({ queryKey: ['llm-providers'] })
    },
  })

  const setDefaultMut = useMutation({
    mutationFn: setDefaultLLMProvider,
    onSuccess: () => {
      message.success('默认供应商已切换')
      qc.invalidateQueries({ queryKey: ['llm-providers'] })
    },
  })

  // Inline test against the saved row.  For unsaved rows the form's "测试连接"
  // button below uses the `id="new"` variant with the form values.
  const runTest = async (id: string) => {
    setTesting(id)
    try {
      const r = await testLLMProvider(id)
      message.success(`连接成功 (${r.model})`)
    } catch (err) {
      message.error((err as Error).message || '连接测试失败')
    } finally {
      setTesting(null)
    }
  }

  const runFormTest = async () => {
    const v = await form.validateFields().catch(() => null)
    if (!v) return
    setTesting('form')
    try {
      // Same wire-encryption rule as save: never put the raw api_key on the
      // network.  encryptSecret('') / encryptSecret(MASK) are no-ops so the
      // backend's "fall back to stored ciphertext" branch still triggers.
      const apiKeyOnWire =
        v.api_key === API_KEY_MASK ? '' : await encryptSecret(v.api_key)
      const r = await testLLMProvider(editing?.id || 'new', {
        provider: v.provider,
        base_url: v.base_url,
        model: v.model,
        api_key: apiKeyOnWire,
      })
      message.success(`连接成功 (${r.model})`)
    } catch (err) {
      message.error((err as Error).message || '连接测试失败')
    } finally {
      setTesting(null)
    }
  }

  const columns = [
    {
      title: '名称',
      dataIndex: 'name',
      render: (name: string, row: LLMProvider) => (
        <Space>
          <Text style={{ fontWeight: 500 }}>{name}</Text>
          {row.is_default && (
            <Tag color="blue" icon={<CheckCircleOutlined />} style={{ fontSize: 11 }}>
              默认
            </Tag>
          )}
        </Space>
      ),
    },
    {
      title: '类型',
      dataIndex: 'provider',
      width: 120,
      render: (p: string, row: LLMProvider) => (
        <Space size={4}>
          <Tag style={{ fontSize: 11 }}>{p}</Tag>
          {row.language && row.language !== 'zh' && (
            <Tag color="purple" style={{ fontSize: 11 }}>{row.language}</Tag>
          )}
        </Space>
      ),
    },
    {
      title: '模型',
      dataIndex: 'model',
      width: 200,
      render: (m: string) => <Text code style={{ fontSize: 11 }}>{m}</Text>,
    },
    {
      title: 'Base URL',
      dataIndex: 'base_url',
      ellipsis: true,
      render: (u: string) =>
        u ? <Text type="secondary" style={{ fontSize: 11 }}>{u}</Text>
          : <Text type="secondary" style={{ fontSize: 11 }}>—</Text>,
    },
    {
      title: '温度',
      dataIndex: 'temperature',
      width: 70,
      render: (t: number) => <Text style={{ fontSize: 11 }}>{t}</Text>,
    },
    {
      title: '启用',
      dataIndex: 'is_enabled',
      width: 70,
      render: (v: boolean) => <Switch checked={v} size="small" disabled />,
    },
    {
      title: '操作',
      width: 220,
      render: (_: unknown, row: LLMProvider) => (
        <Space size={4}>
          <Tooltip title="测试连接">
            <Button
              size="small" type="text"
              icon={<ExperimentOutlined />}
              loading={testing === row.id}
              onClick={() => runTest(row.id)}
            />
          </Tooltip>
          {!row.is_default && (
            <Tooltip title="设为默认（AI 分析将使用此供应商）">
              <Button
                size="small" type="text"
                icon={<ThunderboltOutlined />}
                loading={setDefaultMut.isPending}
                onClick={() => setDefaultMut.mutate(row.id)}
              />
            </Tooltip>
          )}
          <Tooltip title="编辑">
            <Button
              size="small" type="text"
              icon={<EditOutlined />}
              onClick={() => {
                setEditing(row)
                form.setFieldsValue({
                  name: row.name,
                  provider: row.provider,
                  base_url: row.base_url,
                  api_key: API_KEY_MASK,
                  model: row.model,
                  temperature: row.temperature,
                  is_default: row.is_default,
                  is_enabled: row.is_enabled,
                  language: row.language || 'zh',
                  chat_report_max_chars: row.chat_report_max_chars || 8000,
                  chat_history_max_turns: row.chat_history_max_turns || 10,
                })
                setOpen(true)
              }}
            />
          </Tooltip>
          <Popconfirm
            title="确认删除？"
            description={row.is_default ? '此供应商当前为默认，删除后 AI 分析将无法运行。' : undefined}
            okButtonProps={{ danger: true }}
            onConfirm={() => deleteMut.mutate(row.id)}
          >
            <Button size="small" type="text" danger icon={<DeleteOutlined />} />
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <div>
      <PageHeader
        title="AI 大模型配置"
        icon={<RobotOutlined />}
        description="管理 LLM 供应商凭据，用于事件根因分析与对话"
        extra={
          <Button
            type="primary" icon={<PlusOutlined />}
            onClick={() => {
              setEditing(null)
              form.resetFields()
              form.setFieldsValue({
                provider: 'openai',
                temperature: 0.1,
                is_enabled: true,
                is_default: rows.length === 0,
                language: 'zh',
                chat_report_max_chars: 8000,
                chat_history_max_turns: 10,
              })
              setOpen(true)
            }}
          >
            新建
          </Button>
        }
      />

      {rows.length === 0 && !isLoading && (
        <Alert
          type="info" showIcon style={{ marginBottom: 16 }}
          message="尚未配置任何 LLM 供应商"
          description="添加至少一个供应商并设为默认后，事件详情页的「AI 分析」按钮才能正常工作。常见做法：使用 OpenAI 兼容协议接入 DeepSeek / vLLM / 本地 Ollama，base_url 填写各自的 /v1 端点即可。"
        />
      )}

      <SurfaceCard flush>
        <Table
          dataSource={rows} columns={columns} rowKey="id" loading={isLoading}
          size="small" pagination={{ pageSize: 15 }}
        />
      </SurfaceCard>

      <Drawer
        title={editing ? '编辑 LLM 供应商' : '新建 LLM 供应商'}
        open={open}
        size={560}
        onClose={() => { setOpen(false); setEditing(null) }}
        styles={{
          header: { background: c.bgSurface, borderBottom: `1px solid ${c.border}`, color: c.textBody },
          body: { background: c.bgSurface, padding: '20px 24px' },
          footer: { background: c.bgSurface, borderTop: `1px solid ${c.border}` },
        }}
        footer={
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <Button
              icon={<ExperimentOutlined />}
              loading={testing === 'form'}
              onClick={runFormTest}
            >
              测试连接
            </Button>
            <Space>
              <Button onClick={() => { setOpen(false); setEditing(null) }}>取消</Button>
              <Button type="primary" loading={saveMut.isPending} onClick={() => form.submit()}>
                {editing ? '保存' : '创建'}
              </Button>
            </Space>
          </div>
        }
      >
        <Alert
          type="warning" showIcon style={{ marginBottom: 16 }}
          message="api_key 端到端加密，永不以明文出现"
          description="传输：浏览器使用系统 RSA 公钥加密后以 ENC: 前缀提交；存储：服务端落库前再用 AES-256-GCM 加密。列表 / 详情接口仅返回 ******，编辑时保留占位即可不修改密钥。"
        />

        <Form
          form={form}
          layout="vertical"
          onFinish={async (values) => {
            // Two transforms before this leaves the browser:
            //   1. The mask placeholder (user didn't touch the field) is
            //      stripped — the backend then keeps the existing ciphertext.
            //   2. Any real api_key is RSA-encrypted with the system public
            //      key and prefixed with "ENC:".  The server's
            //      DecodeClientCipher peels the prefix and decrypts so the
            //      plaintext key NEVER appears in network captures /
            //      DevTools / proxy logs.
            const payload: Partial<LLMProvider> = { ...values }
            if (payload.api_key === API_KEY_MASK || !payload.api_key) {
              delete payload.api_key
            } else {
              try {
                payload.api_key = await encryptSecret(payload.api_key)
              } catch (err) {
                message.error(
                  (err as Error).message ||
                    '无法加载系统公钥，api_key 无法安全传输',
                )
                return
              }
            }
            saveMut.mutate(payload)
          }}
        >
          <Form.Item
            name="name"
            label="名称"
            rules={[{ required: true, message: '名称必填' }]}
            extra={<span style={{ color: c.textTertiary, fontSize: 11 }}>用于在列表里识别本条配置，例如 deepseek-prod / local-ollama</span>}
          >
            <Input placeholder="deepseek-prod" />
          </Form.Item>

          <Form.Item
            name="provider"
            label="类型"
            rules={[{ required: true, message: '类型必填' }]}
            initialValue="openai"
            extra={<span style={{ color: c.textTertiary, fontSize: 11 }}>OpenAI 兼容服务（DeepSeek / vLLM / Ollama via /v1）请保留 openai，并在下方填写 Base URL</span>}
          >
            <Select>
              {PROVIDER_OPTIONS.map((p) => (
                <Option key={p.value} value={p.value}>{p.label}</Option>
              ))}
            </Select>
          </Form.Item>

          <Form.Item
            name="base_url"
            label={provider === 'qoder' ? 'Qoder API 网关' : 'Base URL'}
            extra={
              <span style={{ color: c.textTertiary, fontSize: 11 }}>
                {provider === 'qoder'
                  ? <>国际站填 <Text code style={{ fontSize: 11 }}>https://api.qoder.com</Text>；中国站填 <Text code style={{ fontSize: 11 }}>https://api.qoder.com.cn</Text></>
                  : <>官方 OpenAI 留空；DeepSeek 填 <Text code style={{ fontSize: 11 }}>https://api.deepseek.com/v1</Text>；
                    Ollama 填 <Text code style={{ fontSize: 11 }}>http://localhost:11434/v1</Text></>}
              </span>
            }
          >
            <Input placeholder={provider === 'qoder' ? 'https://api.qoder.com' : 'https://api.deepseek.com/v1'} />
          </Form.Item>

          <Form.Item
            name="model"
            label={provider === 'qoder' ? 'Agent / Environment' : '模型名称'}
            rules={[{ required: true, message: provider === 'qoder' ? 'Agent 与 Environment 必填' : '模型必填' }]}
            extra={
              <span style={{ color: c.textTertiary, fontSize: 11 }}>
                {provider === 'qoder'
                  ? <>格式 <Text code style={{ fontSize: 11 }}>agent_id|environment_id</Text>，例如 <Text code style={{ fontSize: 11 }}>agent_019e...|env_019e...</Text></>
                  : '例如 gpt-4o-mini / deepseek-chat / qwen2.5:7b-instruct'}
              </span>
            }
          >
            <Input placeholder={provider === 'qoder' ? 'agent_xxx|env_xxx' : 'deepseek-chat'} />
          </Form.Item>

          <Form.Item
            name="api_key"
            label={provider === 'qoder' ? 'PAT（Personal Access Token）' : 'API Key'}
            rules={editing ? [] : [{ required: true, message: provider === 'qoder' ? 'PAT 必填' : 'API Key 必填' }]}
            extra={
              <span style={{ color: c.textTertiary, fontSize: 11 }}>
                {provider === 'qoder'
                  ? '在 Qoder 控制台 → 访问令牌中创建的 PAT。'
                  : (editing
                    ? '保留 ****** 表示不修改；输入新值将通过 RSA 加密传输并重新落库。'
                    : '提交前由浏览器使用系统公钥 RSA 加密（ENC: 前缀），落库时再 AES-256-GCM 加密；列表接口仅返回 ******。')}
              </span>
            }
          >
            <Input.Password placeholder={editing ? API_KEY_MASK : (provider === 'qoder' ? 'qoder_pat_…' : 'sk-…')} autoComplete="new-password" />
          </Form.Item>

          <Form.Item
            name="temperature"
            label="温度 (temperature)"
            initialValue={0.1}
            extra={<span style={{ color: c.textTertiary, fontSize: 11 }}>0 ~ 1，根因分析建议保持较低值 (0 ~ 0.2)</span>}
          >
            <InputNumber style={{ width: '100%' }} min={0} max={2} step={0.1} />
          </Form.Item>

          {/* ─── AI 行为（per-provider，落库后无需重启即可生效） ─────────────── */}
          <div
            style={{
              borderTop: '1px solid #1e1e1e',
              margin: '8px 0 16px',
              paddingTop: 12,
              color: c.textSecondary,
              fontSize: 11,
              letterSpacing: 0.4,
            }}
          >
            AI 行为（每个供应商独立配置）
          </div>

          <Form.Item
            name="language"
            label="输出语言"
            initialValue="zh"
            extra={
              <span style={{ color: c.textTertiary, fontSize: 11 }}>
                根因报告与追问回复使用的语言。<Text code style={{ fontSize: 11 }}>auto</Text> 会跟随告警 / 提问语言；
                ReAct 协议关键字（Thought/Action/...）始终保持英文，不受此项影响。
              </span>
            }
          >
            <Select>
              <Option value="zh">中文（简体）</Option>
              <Option value="en">English</Option>
              <Option value="auto">Auto（跟随用户语言）</Option>
            </Select>
          </Form.Item>

          <Form.Item
            name="chat_report_max_chars"
            label="追问携带的报告最大字数"
            initialValue={8000}
            extra={
              <span style={{ color: c.textTertiary, fontSize: 11 }}>
                追问对话时把上一轮根因报告的前 N 个字符作为上下文喂回模型；过大会撑爆小模型 context window。
                <Text code style={{ fontSize: 11 }}>0</Text> 使用系统默认（8000）。
              </span>
            }
          >
            <InputNumber style={{ width: '100%' }} min={0} max={200000} step={1000} />
          </Form.Item>

          <Form.Item
            name="chat_history_max_turns"
            label="追问保留的历史轮数"
            initialValue={10}
            extra={
              <span style={{ color: c.textTertiary, fontSize: 11 }}>
                每个 incident 的追问对话最多保留最近 N 个 user / assistant 回合作为上下文。
                <Text code style={{ fontSize: 11 }}>0</Text> 使用系统默认（10 轮）。
              </span>
            }
          >
            <InputNumber style={{ width: '100%' }} min={0} max={200} step={1} />
          </Form.Item>

          <div
            style={{
              borderTop: '1px solid #1e1e1e',
              margin: '8px 0 16px',
              paddingTop: 12,
              color: c.textSecondary,
              fontSize: 11,
              letterSpacing: 0.4,
            }}
          >
            启用 / 默认
          </div>

          <Form.Item name="is_default" label="设为默认" valuePropName="checked" extra={
            <span style={{ color: c.textTertiary, fontSize: 11 }}>
              开启后将清除其他配置的默认标记；AI 分析始终使用 is_default + is_enabled 的那一条。
            </span>
          }>
            <Switch />
          </Form.Item>

          <Form.Item name="is_enabled" label="启用" valuePropName="checked" initialValue={true}>
            <Switch />
          </Form.Item>
        </Form>
      </Drawer>
    </div>
  )
}
