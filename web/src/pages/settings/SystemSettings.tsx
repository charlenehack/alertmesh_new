import { useMemo } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Card, Form, Input, Button, Select, App, Alert, Divider, Typography, Space, Tag, Tooltip,
  InputNumber,
} from 'antd'
import {
  LockOutlined, SettingOutlined, ClockCircleOutlined, NotificationOutlined,
  PlusOutlined, MinusCircleOutlined,
} from '@ant-design/icons'
import { getAuthConfig, setAuthConfig, getConfigs, updateConfig } from '../../api/system'
import { encryptJSONSecrets } from '../../api/crypto'
import { useTheme } from '../../hooks/useTheme'
import type { AuthConfig, SystemConfig } from '../../types'

// Top-level JSON fields RSA-encrypted before submit so they never traverse
// the wire in plaintext.  Server (auth.DecodeJSONClientCiphers) detects the
// ENC: prefix and decrypts before AES-encrypting at rest.
const AUTH_CONFIG_SECRET_FIELDS = ['bind_password', 'client_secret']

const { Title, Text } = Typography
const { Option } = Select

export default function SystemSettings() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  const { c } = useTheme()
  const [form] = Form.useForm()

  const { data, isLoading } = useQuery({
    queryKey: ['auth-config'],
    queryFn: getAuthConfig,
  })

  const current: AuthConfig = data ?? { mode: 'local' }

  const saveMut = useMutation({
    mutationFn: async (values: { mode: 'local' | 'ldap' | 'oidc'; config?: string }) => {
      const encryptedConfig = values.config
        ? await encryptJSONSecrets(values.config, AUTH_CONFIG_SECRET_FIELDS)
        : values.config
      return setAuthConfig({ mode: values.mode, config: encryptedConfig })
    },
    onSuccess: () => {
      message.success('认证配置已更新，重启后生效')
      qc.invalidateQueries({ queryKey: ['auth-config'] })
    },
  })

  if (!isLoading && !form.getFieldValue('mode')) {
    form.setFieldsValue({ mode: current.mode })
  }

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 24 }}>
        <SettingOutlined style={{ fontSize: 18, color: c.primary }} />
        <Title level={5} style={{ margin: 0, color: c.textBody }}>系统配置</Title>
      </div>

      <Card
        title={
          <span style={{ color: c.textBody, fontSize: 14 }}>
            <LockOutlined style={{ marginRight: 8 }} />认证方式配置
          </span>
        }
        style={{ background: c.bgSurface, border: `1px solid ${c.borderSubtle}`, borderRadius: 8, maxWidth: 580 }}
      >
        <Alert
          type="info"
          message={<Text style={{ fontSize: 13 }}>当前认证模式：<Text strong style={{ color: '#1677ff' }}>{current.mode?.toUpperCase()}</Text></Text>}
          description="切换认证方式后需重启服务生效。LDAP / OIDC 配置将加密存储。"
          showIcon
          style={{ marginBottom: 24, background: 'rgba(22,119,255,0.08)', border: '1px solid rgba(22,119,255,0.25)' }}
        />

        <Form form={form} layout="vertical" onFinish={saveMut.mutate} initialValues={{ mode: current.mode }}>
          <Form.Item label="认证方式" name="mode" rules={[{ required: true }]}>
            <Select style={{ width: 200 }} loading={isLoading}>
              <Option value="local">本地认证（Local）</Option>
              <Option value="ldap">LDAP</Option>
              <Option value="oidc">OIDC / OAuth2</Option>
            </Select>
          </Form.Item>

          <Form.Item noStyle shouldUpdate={(prev, curr) => prev.mode !== curr.mode}>
            {({ getFieldValue }) => {
              const mode = getFieldValue('mode')
              if (mode === 'ldap') {
                return (
                  <>
                    <Divider style={{ borderColor: c.border, marginBottom: 16 }}>LDAP 配置（JSON）</Divider>
                    <Alert
                      type="warning"
                      message="LDAP 认证功能暂未完整实现，配置将被加密存储但不会生效。"
                      showIcon style={{ marginBottom: 16, background: 'rgba(250,140,22,0.08)', border: '1px solid rgba(250,140,22,0.25)' }}
                    />
                    <Form.Item name="config" rules={[{ required: true, message: '请填写 LDAP 配置' }]}>
                      <Input.TextArea
                        rows={7}
                        placeholder={JSON.stringify({
                          host: 'ldap.example.com', port: 389,
                          base_dn: 'dc=example,dc=com',
                          bind_dn: 'cn=admin,dc=example,dc=com',
                          bind_password: '***', user_filter: '(uid={username})', tls: false,
                        }, null, 2)}
                        style={{ fontFamily: 'monospace', fontSize: 12 }}
                      />
                    </Form.Item>
                  </>
                )
              }
              if (mode === 'oidc') {
                return (
                  <>
                    <Divider style={{ borderColor: c.border, marginBottom: 16 }}>OIDC 配置（JSON）</Divider>
                    <Alert
                      type="warning"
                      message="OIDC 认证功能暂未完整实现，配置将被加密存储但不会生效。"
                      showIcon style={{ marginBottom: 16, background: 'rgba(250,140,22,0.08)', border: '1px solid rgba(250,140,22,0.25)' }}
                    />
                    <Form.Item name="config" rules={[{ required: true, message: '请填写 OIDC 配置' }]}>
                      <Input.TextArea
                        rows={7}
                        placeholder={JSON.stringify({
                          issuer: 'https://auth.example.com',
                          client_id: 'alertmesh', client_secret: '***',
                          redirect_uri: 'http://localhost:8080/callback',
                          scopes: ['openid', 'profile', 'email'],
                        }, null, 2)}
                        style={{ fontFamily: 'monospace', fontSize: 12 }}
                      />
                    </Form.Item>
                  </>
                )
              }
              return null
            }}
          </Form.Item>

          <Form.Item style={{ marginTop: 8 }}>
            <Button type="primary" htmlType="submit" icon={<LockOutlined />} loading={saveMut.isPending}>
              保存认证配置
            </Button>
          </Form.Item>
        </Form>
      </Card>

      <LifecycleSettingsCard />
    </div>
  )
}

// ---------------------------------------------------------------------------
// LifecycleSettingsCard — runtime knobs for the v3 alert lifecycle pipeline.
//
// Three SystemConfig keys, all written through the existing PUT /configs
// endpoint so they take effect immediately (no service restart):
//
//   notification.repeat_schedule   — v3 object {interval_sequence_minutes,
//                                    interval_step_minutes, interval_max_minutes,
//                                    severity_chain[]} consumed by
//                                    maybeRepeatNotify (incident/service.go).
//   incident.staleness_timeout     — Go duration; StartStalenessReaper auto-
//                                    resolves open incidents that have not
//                                    received a new firing alert within
//                                    this window.
//   incident.reopen_window         — Go duration; same group_key arriving
//                                    inside this window after resolved_at
//                                    reopens the original incident instead
//                                    of creating a fresh row.
// ---------------------------------------------------------------------------
const LIFECYCLE_KEYS = {
  schedule: 'notification.repeat_schedule',
  staleness: 'incident.staleness_timeout',
  reopen: 'incident.reopen_window',
} as const

const DURATION_PRESETS = ['0', '5m', '10m', '30m', '1h', '2h', '6h']

// v3 schedule schema mirrored on the frontend.  Kept structurally
// identical to internal/incident/service.go::rawScheduleV3 so the
// payload can be JSON.stringify'd straight onto the SystemConfig row.
type RepeatScheduleV3 = {
  version: 3
  interval_sequence_minutes: number[]
  interval_step_minutes: number
  interval_max_minutes: number
  severity_chain: Array<{
    severity: 'P3' | 'P2' | 'P1' | 'P0'
    dwell: string | null  // Go duration, null = terminal
    tag: string
  }>
}

const DEFAULT_SCHEDULE_V3: RepeatScheduleV3 = {
  version: 3,
  interval_sequence_minutes: [1, 3, 5],
  interval_step_minutes: 2,
  interval_max_minutes: 30,
  severity_chain: [
    { severity: 'P3', dwell: '1h', tag: '[REPEAT]' },
    { severity: 'P2', dwell: '1h', tag: '[ATTENTION]' },
    { severity: 'P1', dwell: '1h', tag: '[ATTENTION]' },
    { severity: 'P0', dwell: null, tag: '[CRITICAL]' },
  ],
}

// Form-state shape — same as RepeatScheduleV3 except dwell is allowed to
// be empty string (rendered as "终态" in the UI).
type ScheduleFormValues = {
  interval_sequence_minutes: number[]
  interval_step_minutes: number
  interval_max_minutes: number
  severity_chain: Array<{
    severity: 'P3' | 'P2' | 'P1' | 'P0'
    dwell: string  // empty string = terminal
    tag: string
  }>
}

function parseScheduleValue(raw: string | undefined): ScheduleFormValues {
  if (!raw) return scheduleToForm(DEFAULT_SCHEDULE_V3)
  try {
    const parsed = JSON.parse(raw)
    if (parsed && typeof parsed === 'object' && Array.isArray(parsed.severity_chain)) {
      return scheduleToForm(parsed as RepeatScheduleV3)
    }
  } catch {
    // legacy v2 array → fall through to default; backend will warn until
    // operator clicks "保存" with the new shape.
  }
  return scheduleToForm(DEFAULT_SCHEDULE_V3)
}

function scheduleToForm(s: RepeatScheduleV3): ScheduleFormValues {
  return {
    interval_sequence_minutes: s.interval_sequence_minutes ?? [],
    interval_step_minutes: s.interval_step_minutes ?? 0,
    interval_max_minutes: s.interval_max_minutes ?? 0,
    severity_chain: (s.severity_chain ?? []).map((t) => ({
      severity: t.severity,
      dwell: t.dwell ?? '',
      tag: t.tag,
    })),
  }
}

function formToScheduleJSON(v: ScheduleFormValues): string {
  const payload: RepeatScheduleV3 = {
    version: 3,
    interval_sequence_minutes: (v.interval_sequence_minutes ?? []).filter((n) => Number.isFinite(n) && n > 0),
    interval_step_minutes: Number(v.interval_step_minutes) || 0,
    interval_max_minutes: Number(v.interval_max_minutes) || 0,
    severity_chain: (v.severity_chain ?? []).map((t) => ({
      severity: t.severity,
      dwell: t.dwell?.trim() ? t.dwell.trim() : null,
      tag: t.tag?.trim() || '',
    })),
  }
  return JSON.stringify(payload)
}

function LifecycleSettingsCard() {
  const qc = useQueryClient()
  const { message } = App.useApp()
  const { c } = useTheme()
  const [form] = Form.useForm()

  const { data, isLoading } = useQuery({
    queryKey: ['configs'],
    queryFn: getConfigs,
  })

  const lookup = useMemo(() => {
    const m: Record<string, string> = {}
    const configs: SystemConfig[] = data ?? []
    for (const c of configs) m[c.key] = c.value
    return m
  }, [data])

  const initialValues = useMemo(() => ({
    schedule: parseScheduleValue(lookup[LIFECYCLE_KEYS.schedule]),
    [LIFECYCLE_KEYS.staleness]: lookup[LIFECYCLE_KEYS.staleness] ?? '0',
    [LIFECYCLE_KEYS.reopen]: lookup[LIFECYCLE_KEYS.reopen] ?? '5m',
  }), [lookup])

  const saveMut = useMutation({
    // /configs PUT stores one row at a time — fan out three sequential
    // calls so a partial failure (e.g. invalid duration on the staleness
    // field) never leaves the schedule half-applied.
    mutationFn: async (values: Record<string, string>) => {
      for (const [key, value] of Object.entries(values)) {
        await updateConfig({ key, value })
      }
    },
    onSuccess: () => {
      message.success('告警生命周期配置已保存')
      qc.invalidateQueries({ queryKey: ['configs'] })
    },
    onError: (err: unknown) => {
      const msg =
        (err as { response?: { data?: { message?: string } } })?.response?.data?.message ?? '保存失败'
      message.error(msg)
    },
  })

  const onFinish = (values: {
    schedule: ScheduleFormValues
    [LIFECYCLE_KEYS.staleness]: string
    [LIFECYCLE_KEYS.reopen]: string
  }) => {
    if (!values.schedule.severity_chain || values.schedule.severity_chain.length === 0) {
      message.error('severity_chain 不能为空')
      return
    }
    saveMut.mutate({
      [LIFECYCLE_KEYS.schedule]: formToScheduleJSON(values.schedule),
      [LIFECYCLE_KEYS.staleness]: values[LIFECYCLE_KEYS.staleness],
      [LIFECYCLE_KEYS.reopen]: values[LIFECYCLE_KEYS.reopen],
    })
  }

  return (
    <Card
      title={
        <span style={{ color: c.textBody, fontSize: 14 }}>
          <NotificationOutlined style={{ marginRight: 8 }} />告警生命周期 (v3)
        </span>
      }
      style={{ background: c.bgSurface, border: `1px solid ${c.borderSubtle}`, borderRadius: 8, maxWidth: 880, marginTop: 24 }}
    >
      <Alert
        type="info"
        message={<Text style={{ fontSize: 13 }}>v3 收敛策略：序列递增间隔 + 驻留触发升级</Text>}
        description={
          <span>
            告警频率按 <Text code>interval_sequence_minutes</Text> 顺序消耗（默认 1m / 3m / 5m），耗尽后每步加
            <Text code> interval_step_minutes</Text>，封顶 <Text code>interval_max_minutes</Text>；在某严重级驻留满
            <Text code> dwell</Text> 即升级到下一档并把序列索引重置为 0。详见 README『告警生命周期与收敛策略』章节。
          </span>
        }
        showIcon
        style={{ marginBottom: 16, background: 'rgba(22,119,255,0.08)', border: '1px solid rgba(22,119,255,0.25)' }}
      />
      <Alert
        type="warning"
        message={<Text style={{ fontSize: 13 }}>升级链合并 + 通知矩阵默认硬编码</Text>}
        description={
          <span>
            v3 已下线 <Text code>escalation_policies</Text> 后台扫描器，升级链统一在
            <Text code> severity_chain</Text> 中维护。Voice / SMS 通道在 dispatcher 层
            <Text strong style={{ color: '#fadb14' }}> 仅对 P0 </Text>
            事件分发（绕过通知策略 severity 过滤），IM / Email 仍按通知策略路由并自动按 channel 合并 @mention / To。
          </span>
        }
        showIcon
        style={{ marginBottom: 24, background: 'rgba(250,173,20,0.08)', border: '1px solid rgba(250,173,20,0.25)' }}
      />

      <Form
        form={form}
        layout="vertical"
        onFinish={onFinish}
        initialValues={initialValues}
        // Re-seed when the query result lands.
        key={JSON.stringify(initialValues)}
      >
        <Divider style={{ borderColor: c.border, margin: '4px 0 16px' }}>
          <Text style={{ fontSize: 12, color: c.textHint }}>重发节奏（notification.repeat_schedule）</Text>
        </Divider>

        <Form.Item
          label={<span>序列间隔（分钟）— interval_sequence_minutes</span>}
          required
          extra={
            <Text type="secondary" style={{ fontSize: 12 }}>
              每个严重级开始时按本数组顺序消耗（默认 <Text code>1, 3, 5</Text>）。耗尽后转入线性递增模式。
            </Text>
          }
        >
          <Form.List name={['schedule', 'interval_sequence_minutes']}>
            {(fields, { add, remove }) => (
              <Space direction="vertical" style={{ width: '100%' }}>
                <Space wrap>
                  {fields.map((field, idx) => (
                    <Space key={field.key} size={4}>
                      <span style={{ color: c.textHint, fontSize: 12 }}>#{idx + 1}</span>
                      <Form.Item
                        {...field}
                        noStyle
                        rules={[{ required: true, message: '需为正数' }]}
                      >
                        <InputNumber min={1} max={1440} style={{ width: 80 }} addonAfter="m" />
                      </Form.Item>
                      <Button
                        size="small"
                        type="text"
                        icon={<MinusCircleOutlined style={{ color: '#888' }} />}
                        onClick={() => remove(field.name)}
                      />
                    </Space>
                  ))}
                </Space>
                <Button
                  size="small"
                  type="dashed"
                  icon={<PlusOutlined />}
                  onClick={() => add(5)}
                  style={{ width: 140 }}
                >
                  添加间隔
                </Button>
              </Space>
            )}
          </Form.List>
        </Form.Item>

        <Space size={24} style={{ display: 'flex' }}>
          <Form.Item
            label="线性递增步长（分钟）"
            name={['schedule', 'interval_step_minutes']}
            extra={
              <Text type="secondary" style={{ fontSize: 12 }}>
                序列耗尽后，每次重发等待时间在上一次基础上 + 本值。
              </Text>
            }
            rules={[{ required: true }]}
          >
            <InputNumber min={1} max={120} style={{ width: 120 }} addonAfter="m" />
          </Form.Item>

          <Form.Item
            label="间隔上限（分钟）"
            name={['schedule', 'interval_max_minutes']}
            extra={
              <Text type="secondary" style={{ fontSize: 12 }}>
                间隔无论怎么递增都不会超过此值。
              </Text>
            }
            rules={[{ required: true }]}
          >
            <InputNumber min={1} max={1440} style={{ width: 120 }} addonAfter="m" />
          </Form.Item>
        </Space>

        <Divider style={{ borderColor: c.border, margin: '8px 0 16px' }}>
          <Text style={{ fontSize: 12, color: c.textHint }}>升级链 — severity_chain</Text>
        </Divider>

        <Alert
          type="info"
          message={
            <Text style={{ fontSize: 12 }}>
              在某档驻留满 <Text code>dwell</Text> 后升级到下一档并重置序列。最后一档 <Text code>dwell</Text> 为空表示终态（不再升级，但继续按节奏重发）。
            </Text>
          }
          showIcon
          style={{ marginBottom: 12, background: 'rgba(22,119,255,0.08)', border: '1px solid rgba(22,119,255,0.25)' }}
        />

        <Form.List name={['schedule', 'severity_chain']}>
          {(fields, { add, remove }) => (
            <>
              {fields.map((field, idx) => (
                <Space key={field.key} align="baseline" style={{ display: 'flex', marginBottom: 8 }}>
                  <span style={{ color: c.textHint, fontSize: 12, width: 24 }}>#{idx + 1}</span>
                  <Form.Item
                    {...field}
                    name={[field.name, 'severity']}
                    rules={[{ required: true, message: '请选择' }]}
                    style={{ marginBottom: 0 }}
                  >
                    <Select style={{ width: 90 }}>
                      {(['P3', 'P2', 'P1', 'P0'] as const).map((s) => (
                        <Option key={s} value={s}>{s}</Option>
                      ))}
                    </Select>
                  </Form.Item>
                  <Form.Item
                    {...field}
                    name={[field.name, 'dwell']}
                    style={{ marginBottom: 0 }}
                    rules={[
                      ({ getFieldValue }) => ({
                        validator: (_: unknown, value: string) => {
                          const isLast = idx === getFieldValue(['schedule', 'severity_chain']).length - 1
                          if (!value || !value.trim()) {
                            return isLast ? Promise.resolve() : Promise.reject(new Error('非终态档需要填 dwell'))
                          }
                          if (/^(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/.test(value.trim())) {
                            return Promise.resolve()
                          }
                          return Promise.reject(new Error('需为 Go duration（如 1h / 30m）'))
                        },
                      }),
                    ]}
                  >
                    <Input style={{ width: 120 }} placeholder="dwell（如 1h，终态留空）" />
                  </Form.Item>
                  <Form.Item
                    {...field}
                    name={[field.name, 'tag']}
                    style={{ marginBottom: 0 }}
                    rules={[{ required: true, message: '请填写 tag' }]}
                  >
                    <Input style={{ width: 160 }} placeholder="[REPEAT] / [ATTENTION] / [CRITICAL]" />
                  </Form.Item>
                  <Button
                    size="small"
                    type="text"
                    icon={<MinusCircleOutlined style={{ color: '#888' }} />}
                    onClick={() => remove(field.name)}
                  />
                </Space>
              ))}
              <Button
                size="small"
                type="dashed"
                icon={<PlusOutlined />}
                onClick={() => add({ severity: 'P3', dwell: '1h', tag: '[REPEAT]' })}
                style={{ marginTop: 4 }}
              >
                添加升级档
              </Button>
            </>
          )}
        </Form.List>

        <Divider style={{ borderColor: c.border, margin: '24px 0 16px' }}>
          <Text style={{ fontSize: 12, color: c.textHint }}>生命周期窗口</Text>
        </Divider>

        <Form.Item
          label={
            <Space size={6}>
              <ClockCircleOutlined />
              <span>陈旧告警自动恢复（incident.staleness_timeout）</span>
            </Space>
          }
          name={LIFECYCLE_KEYS.staleness}
          extra={
            <Text type="secondary" style={{ fontSize: 12 }}>
              Go duration（如 <Text code>10m</Text>、<Text code>30m</Text>）。Open 状态下超过此窗口未再收到告警，
              后台 reaper 将自动标记为已解决并发送 [RESOLVED] 通知。填 <Text code>0</Text> 则禁用自动恢复，
              必须收到明确的恢复消息（如腾讯云 alarmStatus=0）才会标记为已解决。
            </Text>
          }
          rules={[{ required: true, message: '请填写超时时长' }, durationRule]}
        >
          <Input
            style={{ maxWidth: 240 }}
            placeholder="0"
            addonAfter={
              <DurationPresets onPick={(v) => form.setFieldValue(LIFECYCLE_KEYS.staleness, v)} />
            }
          />
        </Form.Item>

        <Form.Item
          label={
            <Space size={6}>
              <ClockCircleOutlined />
              <span>复活窗口（incident.reopen_window）</span>
            </Space>
          }
          name={LIFECYCLE_KEYS.reopen}
          extra={
            <Text type="secondary" style={{ fontSize: 12 }}>
              Go duration（如 <Text code>5m</Text>、<Text code>15m</Text>）。已 resolved 的 incident 在该窗口内
              再次接收到同 group_key 告警 → 复活为 open，发 <Tag color="orange">[REOPENED]</Tag> 通知；超出窗口则新建独立 incident，
              通过 <Text code>parent_incident_id</Text> 关联到旧记录。
            </Text>
          }
          rules={[{ required: true, message: '请填写复活窗口' }, durationRule]}
        >
          <Input
            style={{ maxWidth: 240 }}
            placeholder="5m"
            addonAfter={
              <DurationPresets onPick={(v) => form.setFieldValue(LIFECYCLE_KEYS.reopen, v)} />
            }
          />
        </Form.Item>

        <Form.Item style={{ marginTop: 8 }}>
          <Button
            type="primary"
            htmlType="submit"
            icon={<NotificationOutlined />}
            loading={saveMut.isPending || isLoading}
          >
            保存生命周期配置
          </Button>
        </Form.Item>
      </Form>
    </Card>
  )
}

function DurationPresets({ onPick }: { onPick: (v: string) => void }) {
  return (
    <Tooltip title="快捷填充">
      <Select
        size="small"
        style={{ width: 80 }}
        placeholder="预设"
        options={DURATION_PRESETS.map((d) => ({ value: d, label: d }))}
        onChange={(v: string) => onPick(v)}
      />
    </Tooltip>
  )
}

const durationRule = {
  validator: (_: unknown, value: string) => {
    if (!value) return Promise.resolve()
    const v = value.trim()
    // 0 disables the staleness reaper / reopen window.
    if (v === '0') return Promise.resolve()
    if (/^(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/.test(v)) {
      return Promise.resolve()
    }
    return Promise.reject(new Error('需为 Go duration 字符串，例如 30s / 5m / 2h，填 0 禁用'))
  },
}

