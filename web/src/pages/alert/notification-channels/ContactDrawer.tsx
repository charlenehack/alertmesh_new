import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { App, Button, Drawer, Form, Input, Select, Space } from 'antd'
import { createContactGroup } from '../../../api/alert'
import type { NotificationContact, NotificationContactGroup } from '../../../types'
import { ContactGroupPicker } from './ContactGroupPicker'
import { useTheme } from '../../../hooks/useTheme'

const { Option } = Select

const WEBHOOK_OPTIONS = [
  { key: 'webhook', label: '通用 Webhook' },
  { key: 'slack', label: 'Slack' },
  { key: 'feishu', label: '飞书机器人' },
  { key: 'dingtalk', label: '钉钉机器人' },
] as const

type WebhookKey = typeof WEBHOOK_OPTIONS[number]['key']

interface ContactDrawerProps {
  open: boolean
  editing: NotificationContact | null
  form: ReturnType<typeof Form.useForm<Partial<NotificationContact>>>[0]
  loading: boolean
  onClose: () => void
  onFinish: (v: Partial<NotificationContact>) => void
  allGroups: NotificationContactGroup[]
  allContacts: NotificationContact[]
}

export function ContactDrawer({
  open, editing, form, loading, onClose, onFinish, allGroups, allContacts,
}: ContactDrawerProps) {
  const [objectType, setObjectType] = useState<'contact' | 'group'>('contact')
  const [enabledWebhooks, setEnabledWebhooks] = useState<Set<WebhookKey>>(new Set(['webhook']))
  const { c } = useTheme()

  const handleClose = () => {
    setObjectType('contact')
    setEnabledWebhooks(new Set(['webhook']))
    onClose()
  }

  const toggleWebhook = (key: WebhookKey) => {
    setEnabledWebhooks((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  return (
    <Drawer
      title={editing ? '编辑通知对象' : '创建通知对象'}
      open={open}
      size={640}
      onClose={handleClose}
      styles={{
        header: { background: c.bgSurface, borderBottom: `1px solid ${c.border}`, color: c.textBody },
        body: { background: c.bgSurface, padding: '20px 24px' },
        footer: { background: c.bgSurface, borderTop: `1px solid ${c.border}` },
      }}
      footer={
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <Button onClick={handleClose}>取消</Button>
          <Button
            type="primary" loading={loading} onClick={() => form.submit()}
          >
            确定
          </Button>
        </div>
      }
    >
      {/* 对象类型 toggle (only when creating) */}
      {!editing && (
        <Form.Item
          label={<span style={{ color: c.textBody, fontSize: 13 }}>对象类型</span>}
          required
          style={{ marginBottom: 20 }}
        >
          <Space size={0}>
            <Button
              type={objectType === 'contact' ? 'primary' : 'default'}
              onClick={() => setObjectType('contact')}
              style={objectType === 'contact'
                ? { borderRadius: '6px 0 0 6px' }
                : { borderRadius: '6px 0 0 6px' }}
            >
              联系人
            </Button>
            <Button
              type={objectType === 'group' ? 'primary' : 'default'}
              onClick={() => setObjectType('group')}
              style={objectType === 'group'
                ? { borderRadius: '0 6px 6px 0' }
                : { borderRadius: '0 6px 6px 0' }}
            >
              联系人组
            </Button>
          </Space>
        </Form.Item>
      )}

      {(objectType === 'contact' || editing) && (
        <Form form={form} layout="vertical" onFinish={onFinish}>
          <Form.Item
            name="name"
            label={<span style={{ color: c.textBody, fontSize: 13 }}>名称</span>}
            rules={[
              { required: true, message: '请输入名称' },
              { max: 128, message: '最长 128 个字符' },
              { pattern: /^[a-zA-Z\u4e00-\u9fa5]/, message: '不能以数字或特殊字符开头' },
            ]}
            extra={<NameRules />}
          >
            <Input placeholder="请输入" />
          </Form.Item>

          <Form.Item name="email" label={<span style={{ color: c.textBody, fontSize: 13 }}>邮箱</span>}>
            <Input placeholder="请输入" />
          </Form.Item>

          <Form.Item
            name="phone"
            label={<span style={{ color: c.textBody, fontSize: 13 }}>手机</span>}
            extra={<span style={{ color: c.textHint, fontSize: 11 }}>为避免告警风暴，仅 P0 告警可使用电话对接</span>}
          >
            <Input placeholder="请输入" addonBefore="+86" />
          </Form.Item>

          <div style={{ color: c.textBody, fontSize: 13, fontWeight: 500, marginTop: 16, marginBottom: 10 }}>
            Webhook
          </div>
          <div style={{ display: 'flex', gap: 16, marginBottom: 16 }}>
            {WEBHOOK_OPTIONS.map((w) => {
              const checked = enabledWebhooks.has(w.key)
              return (
                <label
                  key={w.key}
                  onClick={() => toggleWebhook(w.key)}
                  style={{ display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer', fontSize: 13, color: checked ? c.textBody : c.textHint }}
                >
                  <span
                    style={{
                      width: 16, height: 16, borderRadius: 3,
                      display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
                      background: checked ? '#1677ff' : 'transparent',
                      border: checked ? 'none' : `1px solid ${c.border}`,
                    }}
                  >
                    {checked && <span style={{ color: '#fff', fontSize: 11, fontWeight: 700, lineHeight: 1 }}>✓</span>}
                  </span>
                  {w.label}
                </label>
              )
            })}
          </div>

          {enabledWebhooks.has('webhook') && (
            <WebhookFields title="通用 Webhook" urlField="webhook_url" tokenField="webhook_token" tokenPlaceholder="Token（可选，敏感信息加密存储）" />
          )}
          {enabledWebhooks.has('slack') && (
            <WebhookFields title="Slack" tokenField="slack_bot_token" tokenPlaceholder="Bot Token（xoxb-...，加密存储）" extraField="slack_channel_id" extraPlaceholder="Channel ID（如 C01ABCDEFGH）" />
          )}
          {enabledWebhooks.has('feishu') && (
            <WebhookFields title="飞书机器人" urlField="feishu_webhook" tokenField="feishu_secret" tokenPlaceholder="签名校验 Secret（可选，加密存储）" />
          )}
          {enabledWebhooks.has('dingtalk') && (
            <WebhookFields title="钉钉机器人" urlField="dingtalk_webhook" tokenField="dingtalk_secret" tokenPlaceholder="加签 Secret（可选，加密存储）" />
          )}

          <div style={{ color: c.textBody, fontSize: 13, fontWeight: 500, marginTop: 20, marginBottom: 10 }}>
            联系人组
          </div>
          <Form.Item name="group_ids">
            <ContactGroupPicker allGroups={allGroups} allContacts={allContacts} />
          </Form.Item>
        </Form>
      )}

      {objectType === 'group' && !editing && (
        <ContactGroupInlineForm allContacts={allContacts} onClose={handleClose} />
      )}
    </Drawer>
  )
}

// Validation hint shown below the name field.
function NameRules() {
  const { c } = useTheme()
  const rules = [
    '不能以数字、中划线、下划线或其他特殊字符开头',
    '只能包含大小写字母、中文、数字、中划线或下划线',
    '长度限制 1 - 128 以内',
  ]
  return (
    <div style={{ marginTop: 4 }}>
      {rules.map((text) => (
        <div key={text} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 11, color: c.textHint, lineHeight: '20px' }}>
          <span style={{ color: '#52c41a' }}>●</span>{text}
        </div>
      ))}
    </div>
  )
}

interface WebhookFieldsProps {
  title: string
  urlField?: string
  tokenField: string
  tokenPlaceholder: string
  extraField?: string
  extraPlaceholder?: string
}

function WebhookFields({
  title, urlField, tokenField, tokenPlaceholder, extraField, extraPlaceholder,
}: WebhookFieldsProps) {
  const { c } = useTheme()
  return (
    <div style={{ marginBottom: 16 }}>
      <div style={{ color: c.textHint, fontSize: 12, marginBottom: 6 }}>{title}</div>
      {urlField && (
        <Form.Item name={urlField} style={{ marginBottom: 8 }}>
          <Input placeholder="请输入 Webhook URL" />
        </Form.Item>
      )}
      <Form.Item name={tokenField} style={{ marginBottom: extraField ? 8 : 0 }}>
        <Input.Password placeholder={tokenPlaceholder} visibilityToggle />
      </Form.Item>
      {extraField && (
        <Form.Item name={extraField} style={{ marginBottom: 0 }}>
          <Input placeholder={extraPlaceholder} />
        </Form.Item>
      )}
    </div>
  )
}

// Inline contact-group form inside the Drawer when objectType === 'group'.
function ContactGroupInlineForm({
  allContacts, onClose,
}: {
  allContacts: NotificationContact[]
  onClose: () => void
}) {
  const qc = useQueryClient()
  const { message } = App.useApp()
  const { c: gc } = useTheme()
  const [form] = Form.useForm()

  const saveMut = useMutation({
    mutationFn: (v: Partial<NotificationContactGroup>) => createContactGroup(v),
    onSuccess: () => {
      message.success('联系人组创建成功')
      qc.invalidateQueries({ queryKey: ['contact-groups'] })
      qc.invalidateQueries({ queryKey: ['contacts'] })
      form.resetFields()
      onClose()
    },
  })

  return (
    <Form form={form} layout="vertical" onFinish={(v) => saveMut.mutate(v as Partial<NotificationContactGroup>)}>
      <Form.Item
        name="name"
        label={<span style={{ color: gc.textBody, fontSize: 13 }}>分组名称</span>}
        rules={[{ required: true, message: '请输入分组名称' }]}
      >
        <Input placeholder="请输入" />
      </Form.Item>
      <Form.Item
        name="contact_ids"
        label={<span style={{ color: gc.textBody, fontSize: 13 }}>选择联系人</span>}
        rules={[{ required: true, message: '请选择至少一个联系人' }]}
      >
        <Select mode="multiple" placeholder="选择联系人" style={{ width: '100%' }}>
          {allContacts.map((c) => (
            <Option key={c.id} value={c.id}>{c.name}</Option>
          ))}
        </Select>
      </Form.Item>
      <Form.Item name="description" label={<span style={{ color: gc.textBody, fontSize: 13 }}>描述</span>}>
        <Input placeholder="可选描述" />
      </Form.Item>
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 16 }}>
        <Button onClick={onClose}>取消</Button>
        <Button type="primary" htmlType="submit" loading={saveMut.isPending}>
          确定
        </Button>
      </div>
    </Form>
  )
}
