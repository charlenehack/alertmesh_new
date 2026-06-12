/**
 * K8sOverview – 集群概览
 * 显示基础统计 + 资源使用率环形图 + Pod 状态柱状图
 */
import { Row, Col, Alert, Spin, Typography, Tag } from 'antd'
import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import {
  CheckCircleOutlined, CloseCircleOutlined,
  NodeIndexOutlined, AppstoreOutlined,
  DeploymentUnitOutlined, CloudServerOutlined,
} from '@ant-design/icons'
import {
  PieChart, Pie, Cell, BarChart, Bar, XAxis, YAxis, Tooltip as RTip,
  ResponsiveContainer,
} from 'recharts'
import { PageHeader } from '../../components/PageHeader'
import { SurfaceCard } from '../../components/SurfaceCard'
import { useTheme } from '../../hooks/useTheme'
import { useClusters, useSelectedCluster } from './useCluster'
import { http } from '../../api/request'
import { ClusterSelector } from './ClusterSelector'

const { Text, Title: AntTitle } = Typography

const STATUS_COLORS: Record<string, string> = {
  Running: '#52c41a', Pending: '#faad14', Failed: '#ff4d4f',
  Succeeded: '#1677ff', Unknown: '#d9d9d9', Evicted: '#f5222d',
  Terminating: '#fa8c16', CrashLoopBackOff: '#cf1322', OOMKilled: '#a8071a',
  ImagePullBackOff: '#d4b106', ErrImagePull: '#d4b106', Error: '#ff4d4f',
  ContainerCreating: '#1890ff', PodInitializing: '#40a9ff',
}

interface OverviewData {
  total_nodes: number
  ready_nodes: number
  namespace_count: number
  pod_total: number
  deployment_count: number
  daemonset_count: number
  statefulset_count: number
  // resource
  cap_cpu_m: number
  cap_mem_ki: number
  alloc_cpu_m: number
  alloc_mem_ki: number
  usage_cpu_m: number
  usage_mem_ki: number
  cpu_usage_rate: number
  mem_usage_rate: number
  cpu_request_rate: number
  mem_request_rate: number
  metrics_available: boolean
  pod_status_distribution: Record<string, number>
}

function fmtCpu(m: number): string {
  if (m <= 0) return '—'
  if (m >= 1000) return `${(m / 1000).toFixed(1)} 核`
  return `${m}m`
}

function fmtMem(ki: number): string {
  if (ki <= 0) return '—'
  if (ki >= 1024 * 1024) return `${(ki / 1024 / 1024).toFixed(1)} Gi`
  if (ki >= 1024) return `${(ki / 1024).toFixed(1)} Mi`
  return `${ki} Ki`
}

// ─── Ring Gauge Card ──────────────────────────────────────────────────────────

function RingGauge({ value, label, detail, colorMap }: {
  value: number // -1 = N/A
  label: string
  detail: string
  colorMap: (v: number) => string
}) {
  const { isDark } = useTheme()
  const pct = value < 0 ? 0 : Math.min(value, 100)
  const color = value < 0 ? '#d9d9d9' : colorMap(value)

  return (
    <SurfaceCard style={{ textAlign: 'center', padding: '16px 12px' }}>
      <div style={{ position: 'relative', height: 140 }}>
        <ResponsiveContainer width="100%" height={140}>
          <PieChart>
            <Pie
              data={[{ value: pct }, { value: 100 - pct }]}
              dataKey="value"
              cx="50%"
              cy="50%"
              innerRadius={42}
              outerRadius={58}
              startAngle={90}
              endAngle={-270}
              stroke="none"
              animationBegin={0}
              animationDuration={800}
            >
              <Cell fill={color} />
              <Cell fill={isDark ? '#2a2a2a' : '#f0f0f0'} />
            </Pie>
          </PieChart>
        </ResponsiveContainer>
        <div style={{
          position: 'absolute', top: '50%', left: '50%',
          transform: 'translate(-50%, -50%)', pointerEvents: 'none',
        }}>
          <Text style={{ fontSize: 20, fontWeight: 700, color, whiteSpace: 'nowrap' }}>
            {value < 0 ? 'N/A' : `${value.toFixed(1)}%`}
          </Text>
        </div>
      </div>
      <div style={{ fontSize: 13, fontWeight: 500, marginBottom: 2, marginTop: 8 }}>{label}</div>
      <div style={{ fontSize: 11, color: '#999' }}>{detail}</div>
    </SurfaceCard>
  )
}

function usageColor(v: number): string {
  if (v >= 85) return '#ff4d4f'
  if (v >= 70) return '#faad14'
  return '#52c41a'
}

// ─── Main Component ───────────────────────────────────────────────────────────

export default function K8sOverview() {
  const { isDark } = useTheme()
  const { data: clusters } = useClusters()
  const { dsId, select } = useSelectedCluster(clusters)

  const { data, isLoading, error } = useQuery<OverviewData>({
    queryKey: ['k8s-overview', dsId],
    queryFn: () => http.get<OverviewData>(`/k8s/overview?ds=${dsId}`),
    enabled: !!dsId,
    refetchInterval: 30_000,
  })

  // Pod 状态柱状图：使用后端从缓存计算的 pod_status_distribution
  const podChartData = useMemo(() => {
    const dist = data?.pod_status_distribution
    if (!dist || Object.keys(dist).length === 0) return []
    return Object.entries(dist)
      .filter(([, c]) => c > 0)
      .sort((a, b) => b[1] - a[1])
      .map(([name, count]) => ({ name, count, fill: STATUS_COLORS[name] ?? '#8c8c8c' }))
  }, [data?.pod_status_distribution])

  const textColor = isDark ? '#d4d4d4' : '#333'
  const axisColor = isDark ? '#555' : '#ddd'

  return (
    <>
      <PageHeader
        title="集群概览"
        extra={<ClusterSelector clusters={clusters ?? []} value={dsId} onChange={select} />}
      />
      <div style={{ margin: '0 24px 24px' }}>
        {!dsId && <Alert type="info" message="请先在「集群管理」中添加 k8s 数据源，或从上方选择一个集群" />}
        {error && <Alert type="error" message={`获取数据失败: ${(error as Error).message}`} style={{ marginBottom: 12 }} />}
        {isLoading && <Spin style={{ display: 'block', margin: '48px auto' }} />}

        {data && (
          <>
            {/* ── 基础统计卡片 ──────────────────────────────── */}
            <Row gutter={[16, 16]} align="stretch" style={{ marginBottom: 20 }}>
              <Col xs={12} sm={8} md={6} lg={4}>
                <SurfaceCard style={{ padding: '16px 20px', height: '100%' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 12, height: '100%' }}>
                    <NodeIndexOutlined style={{ fontSize: 24, color: '#1677ff' }} />
                    <div>
                      <div style={{ fontSize: 22, fontWeight: 700 }}>{data.total_nodes}</div>
                      <div style={{ fontSize: 12, color: '#999' }}>
                        节点
                        <Tag color={data.ready_nodes === data.total_nodes ? 'success' : 'warning'} style={{ marginLeft: 6, fontSize: 11 }}>
                          {data.ready_nodes}/{data.total_nodes} 可用
                        </Tag>
                      </div>
                    </div>
                  </div>
                </SurfaceCard>
              </Col>
              <Col xs={12} sm={8} md={6} lg={4}>
                <SurfaceCard style={{ padding: '16px 20px', height: '100%' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 12, height: '100%' }}>
                    <AppstoreOutlined style={{ fontSize: 24, color: '#722ed1' }} />
                    <div>
                      <div style={{ fontSize: 22, fontWeight: 700 }}>{data.namespace_count}</div>
                      <div style={{ fontSize: 12, color: '#999' }}>命名空间</div>
                    </div>
                  </div>
                </SurfaceCard>
              </Col>
              <Col xs={12} sm={8} md={6} lg={4}>
                <SurfaceCard style={{ padding: '16px 20px', height: '100%' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 12, height: '100%' }}>
                    <CloudServerOutlined style={{ fontSize: 24, color: '#13c2c2' }} />
                    <div>
                      <div style={{ fontSize: 22, fontWeight: 700 }}>{data.pod_total}</div>
                      <div style={{ fontSize: 12, color: '#999' }}>
                        Pod 总数
                        {(data.pod_status_distribution?.Running ?? 0) > 0 && (
                          <Tag color="success" style={{ marginLeft: 6, fontSize: 11 }}>{data.pod_status_distribution?.Running ?? 0} Running</Tag>
                        )}
                      </div>
                    </div>
                  </div>
                </SurfaceCard>
              </Col>
              <Col xs={12} sm={8} md={6} lg={4}>
                <SurfaceCard style={{ padding: '16px 20px', height: '100%' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 12, height: '100%' }}>
                    <DeploymentUnitOutlined style={{ fontSize: 24, color: '#eb2f96' }} />
                    <div>
                      <div style={{ fontSize: 22, fontWeight: 700 }}>{data.deployment_count}</div>
                      <div style={{ fontSize: 12, color: '#999' }}>Deployments</div>
                    </div>
                  </div>
                </SurfaceCard>
              </Col>
              <Col xs={12} sm={8} md={6} lg={4}>
                <SurfaceCard style={{ padding: '16px 20px', height: '100%' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 12, height: '100%' }}>
                    <CheckCircleOutlined style={{ fontSize: 24, color: '#fa8c16' }} />
                    <div>
                      <div style={{ fontSize: 22, fontWeight: 700 }}>{data.daemonset_count}</div>
                      <div style={{ fontSize: 12, color: '#999' }}>DaemonSets</div>
                    </div>
                  </div>
                </SurfaceCard>
              </Col>
              <Col xs={12} sm={8} md={6} lg={4}>
                <SurfaceCard style={{ padding: '16px 20px', height: '100%' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 12, height: '100%' }}>
                    <CloseCircleOutlined style={{ fontSize: 24, color: '#597ef7' }} />
                    <div>
                      <div style={{ fontSize: 22, fontWeight: 700 }}>{data.statefulset_count}</div>
                      <div style={{ fontSize: 12, color: '#999' }}>StatefulSets</div>
                    </div>
                  </div>
                </SurfaceCard>
              </Col>
            </Row>

            {/* ── 资源使用率环形图 ──────────────────────────── */}
            <AntTitle level={5} style={{ marginBottom: 12 }}>资源使用情况</AntTitle>
            <Row gutter={[16, 16]} style={{ marginBottom: 20 }}>
              <Col xs={24} sm={12} md={6}>
                <RingGauge
                  value={data.cpu_usage_rate}
                  label="CPU 使用率"
                  detail={`使用 ${fmtCpu(data.usage_cpu_m)} / 总量 ${fmtCpu(data.cap_cpu_m)}`}
                  colorMap={usageColor}
                />
              </Col>
              <Col xs={24} sm={12} md={6}>
                <RingGauge
                  value={data.mem_usage_rate}
                  label="内存使用率"
                  detail={`使用 ${fmtMem(data.usage_mem_ki)} / 总量 ${fmtMem(data.cap_mem_ki)}`}
                  colorMap={usageColor}
                />
              </Col>
              <Col xs={24} sm={12} md={6}>
                <RingGauge
                  value={data.cpu_request_rate}
                  label="CPU 分配率"
                  detail={`已分配 ${fmtCpu(data.alloc_cpu_m)} / 总量 ${fmtCpu(data.cap_cpu_m)}`}
                  colorMap={usageColor}
                />
              </Col>
              <Col xs={24} sm={12} md={6}>
                <RingGauge
                  value={data.mem_request_rate}
                  label="内存分配率"
                  detail={`已分配 ${fmtMem(data.alloc_mem_ki)} / 总量 ${fmtMem(data.cap_mem_ki)}`}
                  colorMap={usageColor}
                />
              </Col>
            </Row>

            {!data.metrics_available && (
              <Alert
                type="warning"
                message="Metrics Server 未安装或不可用"
                description="CPU/内存使用率数据需要安装 metrics-server（kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml）"
                showIcon
                style={{ marginBottom: 16 }}
              />
            )}

            {/* ── Pod 状态分布柱状图 ──────────────────────────── */}
            <Row gutter={[16, 16]}>
              <Col xs={24} md={12}>
                <SurfaceCard style={{ padding: '16px 20px' }}>
                  <div style={{ fontSize: 14, fontWeight: 500, marginBottom: 12 }}>Pod 状态分布</div>
                  <ResponsiveContainer width="100%" height={220}>
                    <BarChart data={podChartData} barSize={36}>
                      <XAxis dataKey="name" tick={{ fill: textColor, fontSize: 12 }} axisLine={{ stroke: axisColor }} />
                      <YAxis tick={{ fill: textColor, fontSize: 12 }} axisLine={{ stroke: axisColor }} allowDecimals={false} />
                      <RTip
                        contentStyle={{ background: isDark ? '#1f1f1f' : '#fff', border: 'none', borderRadius: 6 }}
                        labelStyle={{ color: textColor }}
                      />
                      <Bar dataKey="count" radius={[4, 4, 0, 0]}>
                        {podChartData.map((entry, index) => (
                          <Cell key={index} fill={entry.fill} />
                        ))}
                      </Bar>
                    </BarChart>
                  </ResponsiveContainer>
                </SurfaceCard>
              </Col>
              <Col xs={24} md={12}>
                <SurfaceCard style={{ padding: '16px 20px' }}>
                  <div style={{ fontSize: 14, fontWeight: 500, marginBottom: 12 }}>资源概况</div>
                  <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '12px 24px' }}>
                    <ResourceRow label="CPU 规格" value={fmtCpu(data.cap_cpu_m)} />
                    <ResourceRow label="内存规格" value={fmtMem(data.cap_mem_ki)} />
                    <ResourceRow label="CPU 可分配" value={fmtCpu(data.alloc_cpu_m)} />
                    <ResourceRow label="内存可分配" value={fmtMem(data.alloc_mem_ki)} />
                    <ResourceRow label="CPU 使用" value={data.metrics_available ? fmtCpu(data.usage_cpu_m) : 'N/A'} />
                    <ResourceRow label="内存使用" value={data.metrics_available ? fmtMem(data.usage_mem_ki) : 'N/A'} />
                    <ResourceRow label="CPU 使用率" value={data.cpu_usage_rate >= 0 ? `${data.cpu_usage_rate.toFixed(1)}%` : 'N/A'} highlight />
                    <ResourceRow label="内存使用率" value={data.mem_usage_rate >= 0 ? `${data.mem_usage_rate.toFixed(1)}%` : 'N/A'} highlight />
                  </div>
                </SurfaceCard>
              </Col>
            </Row>
          </>
        )}
      </div>
    </>
  )
}

function ResourceRow({ label, value, highlight }: { label: string; value: string; highlight?: boolean }) {
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', padding: '6px 0', borderBottom: '1px solid #f0f0f0' }}>
      <span style={{ fontSize: 13, color: '#888' }}>{label}</span>
      <span style={{ fontSize: 13, fontWeight: highlight ? 600 : 400, fontFamily: 'monospace' }}>{value}</span>
    </div>
  )
}
