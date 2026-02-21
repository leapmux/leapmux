import { onMount } from 'solid-js'
import { AdminSettingsPage } from '~/components/admin/AdminSettingsPage'
import { AuthGuard } from '~/components/common/AuthGuard'

export default function AdminRoute() {
  onMount(() => {
    document.title = 'Administration - LeapMux'
  })
  return (
    <AuthGuard requireAdmin>
      <AdminSettingsPage />
    </AuthGuard>
  )
}
