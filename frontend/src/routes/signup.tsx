import { useNavigate } from '@solidjs/router'
import { onMount } from 'solid-js'
import { SignupPage } from '~/components/common/SignupPage'
import { isSoloMode } from '~/lib/systemInfo'

export default function SignupRoute() {
  const navigate = useNavigate()

  onMount(() => {
    document.title = 'Sign Up - LeapMux'
    if (isSoloMode()) {
      navigate('/o/admin', { replace: true })
    }
  })
  return <SignupPage />
}
