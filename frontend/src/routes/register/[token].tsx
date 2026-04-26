import { useNavigate, useParams } from '@solidjs/router'
import { onMount } from 'solid-js'
import { AuthGuard } from '~/components/common/AuthGuard'
import { RegistrationPage } from '~/components/common/RegistrationPage'
import { setPageTitle } from '~/lib/pageTitle'
import { isSoloMode } from '~/lib/systemInfo'

export default function RegisterRoute() {
  const params = useParams<{ token: string }>()
  const navigate = useNavigate()

  onMount(() => {
    setPageTitle('Register Worker')
    if (isSoloMode()) {
      navigate('/o/admin', { replace: true })
    }
  })
  return (
    <AuthGuard>
      <RegistrationPage token={params.token} />
    </AuthGuard>
  )
}
