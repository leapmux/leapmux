import { onMount } from 'solid-js'
import { ForgotPasswordPage } from '~/components/common/ForgotPasswordPage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { setPageTitle } from '~/lib/pageTitle'

export default function ForgotPasswordRoute() {
  onMount(() => {
    setPageTitle('Forgot password')
  })
  return (
    <SignedOutOnly>
      <ForgotPasswordPage />
    </SignedOutOnly>
  )
}
