import { Suspense, useState } from 'react'
import { Layout, Menu, Avatar, Dropdown, Button, Space, Typography, Tooltip } from 'antd'
import {
  DashboardOutlined,
  AlertOutlined,
  UserOutlined,
  SettingOutlined,
  LogoutOutlined,
  BellOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  MergeCellsOutlined,
  StopOutlined,
  MessageOutlined,
  SoundOutlined,
  NodeIndexOutlined,
  ScheduleOutlined,
  ApiOutlined,
  RobotOutlined,
  DatabaseOutlined,
  SunOutlined,
  MoonOutlined,
  CloudServerOutlined,
  FileTextOutlined,
  SafetyCertificateOutlined,
  ClusterOutlined,
  ContainerOutlined,
  ShareAltOutlined,
  HddOutlined,
  AppstoreOutlined,
    BookOutlined,
    ThunderboltOutlined,
  ControlOutlined,
  BankOutlined,
} from '@ant-design/icons'
import { useNavigate, useLocation, Outlet } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { useAuthStore } from '../store/auth'
import { hasPermission, hasRole, http } from '../api/request'
import { ErrorBoundary } from './ErrorBoundary'
import { RouteFallback } from './RouteFallback'
import type { SystemConfig } from '../types'
import { useThemeMode } from '../theme/ThemeContext'
import { getColors } from '../theme/tokens'

const { Sider, Header, Content } = Layout
const { Text } = Typography

export default function AppLayout() {
  const navigate = useNavigate()
  const location = useLocation()
  const { userInfo, logout } = useAuthStore()
  const [collapsed, setCollapsed] = useState(false)
  const { mode, toggle } = useThemeMode()
  const c = getColors(mode)
  const isDark = mode === 'dark'

  const isAdmin = hasRole('管理员')
  const canOps = isAdmin || hasPermission('opsAccess')
  const canK8s = isAdmin || hasPermission('k8sAccess')
  const canIncident = isAdmin || hasPermission('incidentAccess')
  const canAlertRoute = isAdmin || hasPermission('alertRouteAccess')
  const canAggregation = isAdmin || hasPermission('aggregationAccess')
  const canSilence = isAdmin || hasPermission('silenceAccess')
  const canNotification = isAdmin || hasPermission('notificationAccess')
  const canTemplate = isAdmin || hasPermission('templateAccess')
  const canWebhookSource = isAdmin || hasPermission('webhookSourceAccess')

  // Cached for 5 minutes — system version rarely changes; React Query
  // dedupes the call across remounts (was a bare fetch in useEffect).
  const { data: configs } = useQuery({
    queryKey: ['system-configs'],
    queryFn: () => http.get<SystemConfig[]>('/configs'),
    staleTime: 5 * 60_000,
  })
  const versionConfig = configs?.find((c) => c.key === 'system.version')
  const sysVersion = versionConfig ? 'v' + versionConfig.value : ''

  const handleLogout = () => {
    logout()
    navigate('/login')
  }

  const userMenu = {
    items: [
      {
        key: 'logout',
        icon: <LogoutOutlined />,
        label: '退出登录',
        danger: true,
        onClick: handleLogout,
      },
    ],
  }

  // Build menu items based on role
  const menuItems = [
    {
      key: 'overview',
      icon: <DashboardOutlined />,
      label: '概览',
      onClick: () => navigate('/'),
    },
    {
      key: 'assets',
      icon: <BankOutlined />,
      label: '资产管理',
      children: [
        {
          key: '/assets/tencent',
          icon: <CloudServerOutlined />,
          label: '腾讯云',
          onClick: () => navigate('/assets/tencent'),
        },
        {
          key: '/assets/ctyun',
          icon: <CloudServerOutlined />,
          label: '天翼云',
          onClick: () => navigate('/assets/ctyun'),
        },
        {
          key: '/assets/jd',
          icon: <CloudServerOutlined />,
          label: '京东云',
          onClick: () => navigate('/assets/jd'),
        },
        {
          key: '/assets/datacenter',
          icon: <DatabaseOutlined />,
          label: '本地机房',
          onClick: () => navigate('/assets/datacenter'),
        },
      ],
    },
    ...(canK8s ? [{
      key: 'k8s',
      icon: <ClusterOutlined />,
      label: 'K8s 管理',
      children: [
        {
          key: '/k8s/clusters',
          icon: <ControlOutlined />,
          label: '集群管理',
          onClick: () => navigate('/k8s/clusters'),
        },
        {
          key: '/k8s/overview',
          icon: <AppstoreOutlined />,
          label: '集群概览',
          onClick: () => navigate('/k8s/overview'),
        },
        {
          key: '/k8s/resources',
          icon: <ContainerOutlined />,
          label: '资源管理',
          onClick: () => navigate('/k8s/resources'),
        },
        {
          key: '/k8s/services',
          icon: <ShareAltOutlined />,
          label: '服务路由',
          onClick: () => navigate('/k8s/services'),
        },
        {
          key: '/k8s/nodes',
          icon: <NodeIndexOutlined />,
          label: '节点管理',
          onClick: () => navigate('/k8s/nodes'),
        },

        {
          key: '/k8s/events',
          icon: <BellOutlined />,
          label: '集群事件',
          onClick: () => navigate('/k8s/events'),
        },
      ],
    }] : []),
    ...(canOps ? [{
      key: 'services',
      icon: <CloudServerOutlined />,
      label: '运维操作',
      children: [
        {
          key: '/services/nginx',
          icon: <FileTextOutlined />,
          label: 'Nginx 配置',
          onClick: () => navigate('/services/nginx'),
        },
        {
          key: '/services/sys-init',
          icon: <SettingOutlined />,
          label: '系统初始化',
          onClick: () => navigate('/services/sys-init'),
        },
        {
          key: '/services/waf',
          icon: <SafetyCertificateOutlined />,
          label: 'WAF 配置',
          onClick: () => navigate('/services/waf'),
        },
      ],
    }] : []),
    {
      key: 'alert-center',
      icon: <AlertOutlined />,
      label: '告警中心',
      children: [
        ...(canIncident ? [{
          key: '/incidents',
          icon: <BellOutlined />,
          label: '告警事件',
          onClick: () => navigate('/incidents'),
        }, {
          key: '/alert/reports',
          icon: <FileTextOutlined />,
          label: '告警报告',
          onClick: () => navigate('/alert/reports'),
        }] : []),
        ...(canAlertRoute ? [{
          key: '/alert/routes',
          icon: <NodeIndexOutlined />,
          label: '告警路由',
          onClick: () => navigate('/alert/routes'),
        }] : []),
        ...(canAggregation ? [{
          key: '/alert/aggregations',
          icon: <MergeCellsOutlined />,
          label: '告警聚合策略',
          onClick: () => navigate('/alert/aggregations'),
        }] : []),
        ...(canSilence ? [{
          key: '/alert/silences',
          icon: <StopOutlined />,
          label: '告警静默策略',
          onClick: () => navigate('/alert/silences'),
        }] : []),
        ...(canNotification ? [{
          key: '/alert/channels',
          icon: <SoundOutlined />,
          label: '通知策略',
          onClick: () => navigate('/alert/channels'),
        }] : []),
        ...(canTemplate ? [{
          key: '/alert/templates',
          icon: <MessageOutlined />,
          label: '通知消息模板',
          onClick: () => navigate('/alert/templates'),
        }] : []),
        ...(canWebhookSource ? [{
          key: '/alert/webhook-sources',
          icon: <ApiOutlined />,
          label: 'Webhook 可信源',
          onClick: () => navigate('/alert/webhook-sources'),
        }] : []),
        {
          key: '/observability/query',
          icon: <RobotOutlined />,
          label: '可观测性查询',
          onClick: () => navigate('/observability/query'),
        },
      ],
    },
    // System management – admin only
    ...(isAdmin
      ? [
          {
            key: 'system',
            icon: <SettingOutlined />,
            label: '系统管理',
            children: [
              {
                key: '/users',
                icon: <UserOutlined />,
                label: '用户管理',
                onClick: () => navigate('/users'),
              },
              {
                key: '/roles',
                icon: <SafetyCertificateOutlined />,
                label: '角色管理',
                onClick: () => navigate('/roles'),
              },
              {
                key: '/settings',
                icon: <SettingOutlined />,
                label: '系统配置',
                onClick: () => navigate('/settings'),
              },
              {
                key: '/settings/llm-providers',
                icon: <RobotOutlined />,
                label: 'AI 大模型配置',
                onClick: () => navigate('/settings/llm-providers'),
              },
              {
                key: '/datasources',
                icon: <DatabaseOutlined />,
                label: '数据源',
                onClick: () => navigate('/datasources'),
              },
            ],
          },
        ]
      : []),
  ]

  // Determine selected / open keys from current path
  const selectedKeys = [location.pathname === '/' ? 'overview' : location.pathname]
  // Dynamically open the parent menu that contains the current route
  const defaultOpenKeys = (() => {
    const path = location.pathname
    const keys: string[] = []
    if (path.startsWith('/services')) keys.push('services')
    if (path.startsWith('/k8s')) keys.push('k8s')
    if (path.startsWith('/incidents') || path.startsWith('/alert') || path.startsWith('/observability')) keys.push('alert-center')
    if (isAdmin && (path.startsWith('/users') || path.startsWith('/roles') || path.startsWith('/settings') || path.startsWith('/datasources'))) {
      keys.push('system')
    }
    return keys
  })()

  return (
    <Layout style={{ minHeight: '100vh', background: c.bgPage }}>
      <Sider
        trigger={null}
        collapsible
        collapsed={collapsed}
        width={220}
        style={{
          background: isDark ? '#0d0d0d' : '#ffffff',
          borderRight: `1px solid ${c.borderSubtle}`,
          position: 'fixed',
          height: '100vh',
          left: 0,
          top: 0,
          zIndex: 100,
        }}
      >
        {/* Logo */}
        <div
          style={{
            height: 56,
            display: 'flex',
            alignItems: 'center',
            justifyContent: collapsed ? 'center' : 'flex-start',
            padding: collapsed ? 0 : '0 20px',
            borderBottom: `1px solid ${c.borderSubtle}`,
            gap: 10,
          }}
        >
          <svg width="22" height="22" viewBox="0 0 28 28" fill="none">
            <rect width="28" height="28" rx="5" fill={isDark ? '#ffffff' : '#1677ff'} />
            <path
              d="M7 14h4l3-7 4 14 3-9 2 2h2"
              stroke={isDark ? '#000000' : '#ffffff'}
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
          {!collapsed && (
            <div style={{ display: 'flex', flexDirection: 'column', lineHeight: 1.2 }}>
              <Text style={{ color: c.textStrong, fontSize: 14, fontWeight: 700, letterSpacing: '0.5px' }}>
                运维管理平台
              </Text>
              <Text style={{ color: c.textTertiary, fontSize: 10 }}>Cloud-Hub DevOps</Text>
            </div>
          )}
        </div>

        <Menu
          theme={isDark ? 'dark' : 'light'}
          mode="inline"
          selectedKeys={selectedKeys}
          defaultOpenKeys={defaultOpenKeys}
          items={menuItems}
          style={{
            background: 'transparent',
            borderRight: 'none',
            marginTop: 4,
            fontSize: 13,
            maxHeight: 'calc(100vh - 56px - 40px)',
            overflowY: 'auto',
            overflowX: 'hidden',
          }}
        />

        {!collapsed && sysVersion && (
          <div
            style={{
              position: 'absolute',
              bottom: 16,
              left: 0,
              right: 0,
              textAlign: 'center',
              color: c.textTertiary,
              fontSize: 11,
            }}
          >
            {sysVersion}
          </div>
        )}
      </Sider>

      <Layout
        style={{
          marginLeft: collapsed ? 80 : 220,
          transition: 'margin-left 0.2s',
          background: c.bgPage,
        }}
      >
        <Header
          style={{
            background: isDark ? '#0d0d0d' : '#ffffff',
            padding: '0 20px',
            height: 56,
            lineHeight: '56px',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            borderBottom: `1px solid ${c.borderSubtle}`,
            position: 'sticky',
            top: 0,
            zIndex: 99,
            boxShadow: isDark ? 'none' : '0 1px 4px rgba(0,0,0,0.06)',
          }}
        >
          <Button
            type="text"
            icon={
              collapsed
                ? <MenuUnfoldOutlined style={{ color: c.textTertiary }} />
                : <MenuFoldOutlined style={{ color: c.textTertiary }} />
            }
            onClick={() => setCollapsed(!collapsed)}
            style={{ background: 'transparent' }}
          />

          <Space size={8}>
            {/* Theme toggle */}
            <Tooltip title={isDark ? '切换到亮色模式' : '切换到暗黑模式'}>
              <Button
                type="text"
                icon={
                  isDark
                    ? <SunOutlined style={{ fontSize: 16, color: c.textSecondary }} />
                    : <MoonOutlined style={{ fontSize: 16, color: c.textSecondary }} />
                }
                onClick={toggle}
                style={{
                  background: 'transparent',
                  border: `1px solid ${c.border}`,
                  borderRadius: 6,
                  width: 32,
                  height: 32,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  padding: 0,
                }}
              />
            </Tooltip>

            <Dropdown menu={userMenu} placement="bottomRight">
              <Space style={{ cursor: 'pointer', padding: '4px 8px', borderRadius: 6 }}>
                <Avatar
                  size={28}
                  style={{
                    background: c.bgElevated,
                    color: c.textBody,
                    fontSize: 12,
                    fontWeight: 600,
                    border: `1px solid ${c.border}`,
                  }}
                >
                  {userInfo?.username?.[0]?.toUpperCase() ?? 'U'}
                </Avatar>
                <Text style={{ color: c.textSecondary, fontSize: 13 }}>
                  {userInfo?.username ?? '用户'}
                </Text>
              </Space>
            </Dropdown>
          </Space>
        </Header>

        <Content style={{ padding: 24, background: c.bgPage, minHeight: 'calc(100vh - 56px)' }}>
          <ErrorBoundary resetKey={location.pathname}>
            <Suspense fallback={<RouteFallback />}>
              <Outlet />
            </Suspense>
          </ErrorBoundary>
        </Content>
      </Layout>
    </Layout>
  )
}
