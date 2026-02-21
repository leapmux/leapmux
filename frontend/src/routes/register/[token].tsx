import { useParams } from '@solidjs/router'
import { onMount } from 'solid-js'
import { AuthGuard } from '~/components/common/AuthGuard'
import { RegistrationPage } from '~/components/common/RegistrationPage'

export default function RegisterRoute() {
  const params = useParams<{ token: string }>()
  onMount(() => {
    document.title = 'Register Worker - LeapMux'
  })
  return (
    <AuthGuard>
      <RegistrationPage token={params.token} />
    </AuthGuard>
  )
}
