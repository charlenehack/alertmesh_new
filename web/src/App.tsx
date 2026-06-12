import { lazy } from 'react'
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import {
  MutationCache,
  QueryCache,
  QueryClient,
  QueryClientProvider,
} from '@tanstack/react-query'
import { ConfigProvider, App as AntApp, Spin } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
import 'dayjs/locale/zh-cn'

import AppLayout from './components/AppLayout'
import Login from './pages/Login'
import { surfaceApiError } from './api/notify'
import { useUserInfoHydration } from './hooks/useUserInfoHydration'
import { getAntdTheme } from './theme/antdTheme'
import { ThemeProvider, useThemeMode } from './theme/ThemeContext'
import { getColors } from './theme/tokens'

const Dashboard = lazy(() => import('./pages/Dashboard'))
const IncidentList = lazy(() => import('./pages/incidents/IncidentList'))
const IncidentDetail = lazy(() => import('./pages/incidents/incident-detail'))
const NotificationChannels = lazy(() => import('./pages/alert/notification-channels'))
const NotificationTemplates = lazy(() => import('./pages/alert/NotificationTemplates'))
const AlertRoutes = lazy(() => import('./pages/alert/AlertRoutes'))
const AggregationPolicies = lazy(() => import('./pages/alert/AggregationPolicies'))
const SilencePolicies = lazy(() => import('./pages/alert/SilencePolicies'))
const WebhookSources = lazy(() => import('./pages/alert/WebhookSources'))
const UserList = lazy(() => import('./pages/users/UserList'))
const RoleList = lazy(() => import('./pages/users/RoleList'))
const SystemSettings = lazy(() => import('./pages/settings/SystemSettings'))
const LLMProviders = lazy(() => import('./pages/settings/LLMProviders'))
const DataSources = lazy(() => import('./pages/datasources/DataSources'))
const PromExplore = lazy(() => import('./pages/datasources/PromExplore'))
const NginxConfig = lazy(() => import('./pages/services/NginxConfig'))
const SysInit = lazy(() => import('./pages/services/SysInit'))
const WafConfig = lazy(() => import('./pages/services/WafConfig'))
const TencentCloud = lazy(() => import('./pages/assets/TencentCloud'))
const CtyunCloud = lazy(() => import('./pages/assets/CtyunCloud'))
const JdCloud = lazy(() => import('./pages/assets/JdCloud'))
const Datacenter = lazy(() => import('./pages/assets/Datacenter'))
const K8sClusters = lazy(() => import('./pages/k8s/K8sClusters'))
const K8sOverview = lazy(() => import('./pages/k8s/K8sOverview'))
const K8sPods = lazy(() => import('./pages/k8s/K8sPods'))
const K8sServiceRoutes = lazy(() => import('./pages/k8s/K8sServiceRoutes'))
const K8sNodes = lazy(() => import('./pages/k8s/K8sNodes'))
const K8sEvents = lazy(() => import('./pages/k8s/K8sEvents'))

dayjs.extend(relativeTime)
dayjs.locale('zh-cn')

const qc = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      staleTime: 10_000,
      gcTime: 5 * 60_000,
      refetchOnWindowFocus: false,
    },
    mutations: { retry: 0 },
  },
  queryCache: new QueryCache({ onError: surfaceApiError }),
  mutationCache: new MutationCache({ onError: surfaceApiError }),
})

function RequireAuth({ children }: { children: React.ReactNode }) {
  const { token, hydrating } = useUserInfoHydration()
  const { mode } = useThemeMode()
  const bgPage = getColors(mode).bgPage

  if (!token) return <Navigate to="/login" replace />
  if (hydrating) {
    return (
      <div
        style={{
          minHeight: '100vh',
          background: bgPage,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
        }}
      >
        <Spin />
      </div>
    )
  }
  return <>{children}</>
}

function ThemedApp() {
  const { mode } = useThemeMode()
  return (
    <ConfigProvider locale={zhCN} theme={getAntdTheme(mode)}>
      <AntApp>
        <BrowserRouter>
          <Routes>
            <Route path="/login" element={<Login />} />
            <Route
              path="/"
              element={
                <RequireAuth>
                  <AppLayout />
                </RequireAuth>
              }
            >
              <Route index element={<Dashboard />} />

              {/* Alert Center */}
              <Route path="incidents" element={<IncidentList />} />
              <Route path="incidents/:id" element={<IncidentDetail />} />
              <Route path="alert/routes" element={<AlertRoutes />} />
              <Route path="alert/aggregations" element={<AggregationPolicies />} />
              <Route path="alert/silences" element={<SilencePolicies />} />
              <Route path="alert/channels" element={<NotificationChannels />} />
              <Route path="alert/templates" element={<NotificationTemplates />} />
              <Route path="alert/webhook-sources" element={<WebhookSources />} />

              {/* System – admin only (frontend guards in AppLayout) */}
              <Route path="users" element={<UserList />} />
              <Route path="roles" element={<RoleList />} />
              <Route path="settings" element={<SystemSettings />} />
              <Route path="settings/llm-providers" element={<LLMProviders />} />
              <Route path="datasources" element={<DataSources />} />
              <Route path="datasources/:id/prom-explore" element={<PromExplore />} />

              {/* Services */}
              <Route path="services/nginx" element={<NginxConfig />} />
              <Route path="services/sys-init" element={<SysInit />} />
              <Route path="services/waf" element={<WafConfig />} />

              {/* Assets */}
              <Route path="assets/tencent" element={<TencentCloud />} />
              <Route path="assets/ctyun" element={<CtyunCloud />} />
              <Route path="assets/jd" element={<JdCloud />} />
              <Route path="assets/datacenter" element={<Datacenter />} />

              {/* K8s Management */}
              <Route path="k8s/clusters" element={<K8sClusters />} />
              <Route path="k8s/overview" element={<K8sOverview />} />
              <Route path="k8s/resources" element={<K8sPods />} />
              <Route path="k8s/services" element={<K8sServiceRoutes />} />
              <Route path="k8s/nodes" element={<K8sNodes />} />
              <Route path="k8s/events" element={<K8sEvents />} />
            </Route>
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </BrowserRouter>
      </AntApp>
    </ConfigProvider>
  )
}

export default function App() {
  return (
    <QueryClientProvider client={qc}>
      <ThemeProvider>
        <ThemedApp />
      </ThemeProvider>
    </QueryClientProvider>
  )
}
