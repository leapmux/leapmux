import { onMount } from 'solid-js'
import { ForgotPasswordPage } from '~/components/common/ForgotPasswordPage'
import { setPageTitle } from '~/lib/pageTitle'

export default function ForgotPasswordRoute() {
  onMount(() => {
    setPageTitle('Forgot password')
  })
  return <ForgotPasswordPage />
}
