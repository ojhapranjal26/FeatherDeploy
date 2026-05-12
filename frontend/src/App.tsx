import { Navigate, Route, Routes } from 'react-router-dom'
import { AppLayout } from '@/components/layout/AppLayout'
import { ProtectedRoute } from '@/components/ProtectedRoute'
import { LoginPage } from '@/pages/LoginPage'
import { InviteAcceptPage } from '@/pages/InviteAcceptPage'
import { QRApprovePage } from '@/pages/QRLoginPage'
import { DashboardPage } from '@/pages/DashboardPage'
import { ProjectPage } from '@/pages/ProjectPage'
import { ServicePage } from '@/pages/ServicePage'
import { DeploymentListPage } from '@/pages/DeploymentListPage'
import { DeploymentDetailPage } from '@/pages/DeploymentDetailPage'
import { EnvPage } from '@/pages/EnvPage'
import { DomainsPage } from '@/pages/DomainsPage'
import { AdminUsersPage } from '@/pages/AdminUsersPage'
import { AdminSettingsPage } from '@/pages/AdminSettingsPage'
import { GitHubSettingsPage } from '@/pages/GitHubSettingsPage'
import { NodesPage } from '@/pages/NodesPage'
import { ProjectsListPage } from '@/pages/ProjectsListPage'
import { DevicesPage } from '@/pages/DevicesPage'
import { UserSettingsPage } from '@/pages/UserSettingsPage'
import { StoragePage } from '@/pages/StoragePage'
import { StorageDetailPage } from '@/pages/StorageDetailPage'
import { ClusterPage } from '@/pages/ClusterPage'

export default function App() {
  return (
    <Routes>
      {/* Public routes */}
      <Route path="/login" element={<LoginPage />} />
      <Route path="/invite/:token" element={<InviteAcceptPage />} />

      {/* Protected routes wrapped in AppLayout */}
      <Route element={<ProtectedRoute />}>
        {/* QR approve page — authenticated device approves a pending QR login */}
        <Route path="qr-approve/:token" element={<QRApprovePage />} />
        <Route element={<AppLayout />}>
          <Route index element={<Navigate to="/dashboard" replace />} />
          <Route path="dashboard" element={<DashboardPage />} />
          <Route path="cluster" element={<ClusterPage />} />
          <Route path="devices" element={<DevicesPage />} />
          <Route path="settings/profile" element={<UserSettingsPage />} />
          <Route path="projects" element={<ProjectsListPage />} />

          <Route path="projects/:projectId" element={<ProjectPage />} />
          <Route path="projects/:projectId/services/:serviceId" element={<ServicePage />} />
          <Route
            path="projects/:projectId/services/:serviceId/deployments"
            element={<DeploymentListPage />}
          />
          <Route
            path="projects/:projectId/services/:serviceId/deployments/:deploymentId"
            element={<DeploymentDetailPage />}
          />
          <Route
            path="projects/:projectId/services/:serviceId/env"
            element={<EnvPage />}
          />
          <Route
            path="projects/:projectId/services/:serviceId/domains"
            element={<DomainsPage />}
          />

          {/* GitHub integration (all authenticated users) */}
          <Route path="settings/github" element={<GitHubSettingsPage />} />

          {/* Admin routes — visible only to admin and superadmin */}
          <Route element={<ProtectedRoute requiredRoles={['admin', 'superadmin']} />}>
            <Route path="admin/users" element={<AdminUsersPage />} />
            <Route path="admin/nodes" element={<NodesPage />} />
            <Route path="storage" element={<StoragePage />} />
            <Route path="storage/:storageId" element={<StorageDetailPage />} />
          </Route>

          {/* Superadmin-only routes */}
          <Route element={<ProtectedRoute requiredRoles={['superadmin']} />}>
            <Route path="admin/settings" element={<AdminSettingsPage />} />
          </Route>
        </Route>
      </Route>

      {/* Fallback */}
      <Route path="*" element={<Navigate to="/dashboard" replace />} />
    </Routes>
  )
}
