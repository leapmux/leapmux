import { useNavigate } from '@solidjs/router'
import { onMount } from 'solid-js'
import { SignupPage } from '~/components/common/SignupPage'
import { setPageTitle } from '~/lib/pageTitle'
import { isSoloMode } from '~/lib/systemInfo'

export default function SignupRoute() {
  const navigate = useNavigate()

  onMount(() => {
    setPageTitle('Sign Up')
    if (isSoloMode()) {
      navigate('/o/admin', { replace: true })
    }
  })
  return <SignupPage />
}
