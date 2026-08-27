import { onMount } from 'solid-js'
import { SetupPage } from '~/components/common/SetupPage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { setPageTitle } from '~/lib/pageTitle'

export default function SetupRoute() {
  onMount(() => {
    setPageTitle('Setup')
  })
  // Wrapped like its four siblings, and answering a different question than
  // SetupGate does. SetupGate asks whether this HUB still needs a first
  // administrator; this asks whether this BROWSER is already signed in, in
  // which case the remedy is Preferences -> Account rather than a second
  // account.
  return (
    <SignedOutOnly>
      <SetupPage />
    </SignedOutOnly>
  )
}
