import { useNavigate } from '@solidjs/router'
import { createEffect, onMount } from 'solid-js'
import { SignupPage } from '~/components/common/SignupPage'
import { useAuth } from '~/context/AuthContext'
import { setPageTitle } from '~/lib/pageTitle'
import { isSoloMode } from '~/lib/systemInfo'

export default function SignupRoute() {
  const navigate = useNavigate()
  const auth = useAuth()

  onMount(() => setPageTitle('Sign Up'))

  // Same gate, and same reason, as LoginPage: isSoloMode() is a plain module
  // variable that reads `false` until loadSystemInfo() resolves, so checking it
  // from onMount meant the redirect silently never fired on a cold load and a
  // solo-hub visitor got a signup form the hub has no endpoint for.
  createEffect(() => {
    if (auth.loading()) {
      return
    }
    if (isSoloMode()) {
      navigate('/', { replace: true })
    }
  })

  return <SignupPage />
}
