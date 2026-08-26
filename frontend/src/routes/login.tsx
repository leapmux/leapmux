import { onMount } from 'solid-js'
import { LoginPage } from '~/components/common/LoginPage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { setPageTitle } from '~/lib/pageTitle'

export default function LoginRoute() {
  onMount(() => {
    setPageTitle('Login')
  })
  return (
    <SignedOutOnly>
      <LoginPage />
    </SignedOutOnly>
  )
}
