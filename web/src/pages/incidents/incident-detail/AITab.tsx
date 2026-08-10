import type { RefObject } from 'react'
import { Alert, Button, Card, Col, Descriptions, Divider, Empty, Input, Row, Space, Spin, Tag, Typography } from 'antd'
import {
  BulbOutlined, ClockCircleOutlined, FileTextOutlined,
  MessageOutlined, ReloadOutlined, RobotOutlined, SendOutlined,
  ThunderboltOutlined, UnorderedListOutlined,
} from '@ant-design/icons'
import dayjs from 'dayjs'
import MarkdownView from '../../../components/MarkdownView'
import SeverityBadge from '../../../components/SeverityBadge'
import { extractHeadings } from '../../../components/markdownHeadings'
import type { Incident, Severity } from '../../../types'
import { ReportTOC } from './ReportTOC'
import { estimateWords } from './helpers'
import type { AIReportData } from './types'

const { Text } = Typography

interface AITabProps {
  inc: Incident
  aiReport?: AIReportData
  aiInfo: { color: string; text: string }
  wsMessages: Array<{ type: string; content: string }>
  chatInput: string
  setChatInput: (v: string) => void
  chatLoading: boolean
  chatEndRef: RefObject<HTMLDivElement | null>
  handleChat: () => void
  onTrigger: () => void
  triggerLoading: boolean
}

// Layout: 2-column Row (xs:single column, lg:24 split into 17/7).
//   Left  (col 17): big Card with the Markdown report.
//   Right (col 7):  the 标题栏 sidebar — analysis status, model meta, quick
//                   stats and the 重新分析 button.
// Below both columns: the chat / 追问 area spans full width because long
// conversation threads need horizontal room for code blocks.
export function AITab(p: AITabProps) {
  const {
    inc, aiReport, aiInfo, wsMessages, chatInput, setChatInput,
    chatLoading, chatEndRef, handleChat, onTrigger, triggerLoading,
  } = p

  const reportReady = !!aiReport?.report
  const reportChars = aiReport?.report?.length ?? 0
  const reportWords = aiReport?.report ? estimateWords(aiReport.report) : 0
  const convoCount = aiReport?.conversations?.length ?? 0
  const reportTime = aiReport?.created_at ? dayjs(aiReport.created_at) : null
  const headings = reportReady ? extractHeadings(aiReport.report!) : []

  return (
    <div>
      <Row gutter={16}>
        {/* ───────── LEFT: the Markdown report ───────── */}
        <Col xs={24} lg={17}>
          <Card
            style={{ borderRadius: 12, border: 'none', boxShadow: '0 2px 12px rgba(0,0,0,0.06)', marginBottom: 16 }}
            styles={{ body: { padding: '20px 24px' } }}
            title={
              <Space size={8}>
                <FileTextOutlined style={{ color: '#722ed1' }} />
                <span style={{ fontWeight: 600 }}>AI 分析报告</span>
                {reportReady && <Tag color="purple" style={{ marginLeft: 4 }}>Markdown</Tag>}
              </Space>
            }
          >
            {inc.ai_status === 'pending' && !reportReady && (
              <Empty
                description="AI 分析尚未启动，点击右侧「重新分析」按钮开始"
                image={<RobotOutlined style={{ fontSize: 48, color: '#d9d9d9' }} />}
              />
            )}

            {inc.ai_status === 'running' && !reportReady && (
              <div style={{ textAlign: 'center', padding: 40 }}>
                <Spin tip="AI 正在分析中，请稍候..." size="large">
                  <div style={{ padding: 20 }}>
                    {wsMessages.filter((m) => m.type === 'agent_action').map((m, i) => {
                      try {
                        const action = JSON.parse(m.content) as { tool?: string }
                        return (
                          <div key={i} style={{ color: '#999', fontSize: 12, marginTop: 8 }}>
                            正在调用工具: {action.tool}
                          </div>
                        )
                      } catch { return null }
                    })}
                  </div>
                </Spin>
              </div>
            )}

            {reportReady && <MarkdownView source={aiReport.report!} />}

            {inc.ai_status === 'failed' && !reportReady && (
              <Alert
                type="error"
                message="AI 分析失败"
                description={
                  <div>
                    <div>
                      {wsMessages.filter((m) => m.type === 'analysis_error').slice(-1)[0]?.content
                        ?? '请检查 LLM Provider 配置是否正确，或点击右侧「重新分析」重试。'}
                    </div>
                    <div style={{ marginTop: 8, fontSize: 12, color: '#999' }}>
                      提示：检查系统设置中的默认 LLM Provider 是否启用、模型名称和 API Key 是否正确。
                    </div>
                  </div>
                }
                showIcon
              />
            )}
          </Card>
        </Col>

        {/* ───────── RIGHT: the 标题栏 / 元信息侧边栏 ───────── */}
        <Col xs={24} lg={7}>
          <Card
            style={{ borderRadius: 12, border: 'none', boxShadow: '0 2px 12px rgba(0,0,0,0.06)', marginBottom: 16, position: 'sticky', top: 16 }}
            styles={{ body: { padding: 0 } }}
          >
            <div
              style={{
                background: 'linear-gradient(135deg, #722ed1 0%, #5b21b6 100%)',
                color: '#fff',
                padding: '14px 18px',
                borderRadius: '12px 12px 0 0',
              }}
            >
              <Space size={8}>
                <RobotOutlined style={{ fontSize: 18 }} />
                <span style={{ fontSize: 15, fontWeight: 600 }}>AI 分析详情</span>
              </Space>
              <div style={{ marginTop: 6, fontSize: 12, opacity: 0.85 }}>
                Root-cause analysis powered by LLM
              </div>
            </div>

            <div style={{ padding: '14px 18px' }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 10 }}>
                <Text type="secondary" style={{ fontSize: 12 }}>当前状态</Text>
                <span style={{ color: aiInfo.color, fontWeight: 600, fontSize: 13 }}>
                  <ThunderboltOutlined /> {aiInfo.text}
                </span>
              </div>

              <Divider style={{ margin: '8px 0 12px' }} />

              <Descriptions column={1} size="small" colon={false}
                labelStyle={{ color: '#8c8c8c', fontSize: 12, width: 86 }}
                contentStyle={{ fontSize: 12.5 }}
              >
                <Descriptions.Item label={<Space size={4}><ClockCircleOutlined />生成时间</Space>}>
                  {reportTime ? reportTime.format('MM-DD HH:mm') : '—'}
                </Descriptions.Item>
                <Descriptions.Item label={<Space size={4}><FileTextOutlined />报告长度</Space>}>
                  {reportReady ? `${reportChars.toLocaleString()} 字符 · 约 ${reportWords.toLocaleString()} 字` : '—'}
                </Descriptions.Item>
                <Descriptions.Item label={<Space size={4}><MessageOutlined />追问轮次</Space>}>
                  {convoCount > 0 ? `${Math.ceil(convoCount / 2)} 轮（${convoCount} 条）` : '—'}
                </Descriptions.Item>
                <Descriptions.Item label={<Space size={4}><BulbOutlined />事件级别</Space>}>
                  <SeverityBadge severity={inc.severity as Severity} />
                </Descriptions.Item>
              </Descriptions>

              {aiReport?.summary && (
                <>
                  <Divider style={{ margin: '12px 0' }} />
                  <Text type="secondary" style={{ fontSize: 12 }}>结论摘要</Text>
                  <div style={{ marginTop: 6, fontSize: 12.5, lineHeight: 1.7, color: '#d8d8d8' }}>
                    {aiReport.summary}
                  </div>
                </>
              )}

              {aiReport?.root_cause && (
                <>
                  <Divider style={{ margin: '12px 0' }} />
                  <Text type="secondary" style={{ fontSize: 12 }}>根因</Text>
                  <div style={{ marginTop: 6, fontSize: 12.5, lineHeight: 1.7, color: '#d8d8d8' }}>
                    {aiReport.root_cause}
                  </div>
                </>
              )}

              <Divider style={{ margin: '14px 0 10px' }} />
              <Button
                block
                icon={<ReloadOutlined />}
                loading={triggerLoading}
                onClick={onTrigger}
                disabled={inc.ai_status === 'running'}
                style={{ borderColor: '#722ed1', color: '#722ed1' }}
              >
                {reportReady ? '重新分析' : '触发 AI 分析'}
              </Button>
            </div>
          </Card>

          {/* TOC / 大纲 — only shown once the report has any headings. */}
          {headings.length > 0 && (
            <Card
              size="small"
              style={{
                borderRadius: 12,
                border: 'none',
                boxShadow: '0 2px 12px rgba(0,0,0,0.06)',
                marginBottom: 16,
                position: 'sticky',
                top: 360,
              }}
              styles={{ body: { padding: '12px 14px' } }}
              title={
                <Space size={6} style={{ fontSize: 13 }}>
                  <UnorderedListOutlined />
                  <span>大纲</span>
                  <Tag style={{ marginLeft: 2, fontSize: 11, padding: '0 6px' }}>{headings.length}</Tag>
                </Space>
              }
            >
              <ReportTOC headings={headings} />
            </Card>
          )}
        </Col>
      </Row>

      {/* Conversation history — full width below */}
      {convoCount > 0 && (
        <Card
          title={<Space size={8}><MessageOutlined />追问对话</Space>}
          size="small"
          style={{ borderRadius: 12, border: 'none', boxShadow: '0 2px 12px rgba(0,0,0,0.06)', marginBottom: 16 }}
        >
          <div style={{ maxHeight: 480, overflowY: 'auto', padding: '8px 0' }}>
            {aiReport!.conversations!.map((conv) => (
              <div
                key={conv.id}
                style={{
                  display: 'flex',
                  justifyContent: conv.role === 'user' ? 'flex-end' : 'flex-start',
                  marginBottom: 12,
                }}
              >
                <div
                  style={{
                    maxWidth: '82%',
                    padding: '10px 14px',
                    borderRadius: 10,
                    background: conv.role === 'user' ? '#722ed1' : '#f8f8fc',
                    border: conv.role === 'user' ? 'none' : '1px solid #e9e0f8',
                  }}
                >
                  <MarkdownView source={conv.content} inverted={conv.role === 'user'} />
                  <div style={{
                    fontSize: 11,
                    marginTop: 6,
                    color: conv.role === 'user' ? 'rgba(255,255,255,0.65)' : '#666',
                    textAlign: 'right',
                  }}>
                    {dayjs(conv.created_at).format('MM-DD HH:mm:ss')}
                  </div>
                </div>
              </div>
            ))}
            <div ref={chatEndRef} />
          </div>
        </Card>
      )}

      {/* Chat input */}
      {(inc.ai_status === 'done' || reportReady) && (
        <Card
          size="small"
          style={{ borderRadius: 12, border: 'none', boxShadow: '0 2px 12px rgba(0,0,0,0.06)' }}
        >
          <div style={{ display: 'flex', gap: 8 }}>
            <Input
              placeholder="对 AI 分析结果提出追问，回车发送..."
              value={chatInput}
              onChange={(e) => setChatInput(e.target.value)}
              onPressEnter={handleChat}
              disabled={chatLoading}
            />
            <Button
              type="primary"
              icon={<SendOutlined />}
              onClick={handleChat}
              loading={chatLoading}
              style={{ background: '#722ed1', borderColor: '#722ed1' }}
            >
              发送
            </Button>
          </div>
        </Card>
      )}
    </div>
  )
}
