import { onMount } from 'solid-js'
import { HubPage } from '~/components/common/HubPage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { SignupPage } from '~/components/common/SignupPage'
import { setPageTitle } from '~/lib/pageTitle'

export default function SignupRoute() {
  onMount(() => setPageTitle('Sign Up'))
  return (
    <SignedOutOnly>
      <HubPage>
        <SignupPage />
      </HubPage>
    </SignedOutOnly>
  )
}
