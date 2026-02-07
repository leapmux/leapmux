import { onMount } from 'solid-js'
import { AuthGuard } from '~/components/common/AuthGuard'
import { PreferencesPage } from '~/components/settings/PreferencesPage'

export default function SettingsRoute() {
  onMount(() => {
    document.title = 'Preferences - LeapMux'
  })
  return (
    <AuthGuard>
      <PreferencesPage />
    </AuthGuard>
  )
}
