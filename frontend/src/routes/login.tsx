import { onMount } from 'solid-js'
import { HubPage } from '~/components/common/HubPage'
import { LoginPage } from '~/components/common/LoginPage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { BrowserSignIn } from '~/components/desktop/BrowserSignIn'
import { setPageTitle } from '~/lib/pageTitle'

export default function LoginRoute() {
  onMount(() => {
    setPageTitle('Login')
  })
  return (
    <SignedOutOnly>
      <HubPage inDesktop={() => <BrowserSignIn />}>
        <LoginPage />
      </HubPage>
    </SignedOutOnly>
  )
}
