import { onMount } from 'solid-js'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { SignupPage } from '~/components/common/SignupPage'
import { setPageTitle } from '~/lib/pageTitle'

export default function SignupRoute() {
  onMount(() => setPageTitle('Sign Up'))

  // The solo-hub redirect lives in `SignedOutOnly`, which wraps every
  // credential page. This route kept a copy of it, and so did LoginPage,
  // while /recover-account, /recover-account/complete and /setup carried none.
  return (
    <SignedOutOnly>
      <SignupPage />
    </SignedOutOnly>
  )
}
