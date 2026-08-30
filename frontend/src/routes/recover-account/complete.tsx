import { onMount } from 'solid-js'
import { RecoverCompletePage } from '~/components/common/RecoverCompletePage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { setPageTitle } from '~/lib/pageTitle'

export default function RecoverAccountCompleteRoute() {
  onMount(() => {
    setPageTitle('Choose a new password')
  })
  return (
    // explain, not the default redirect: this address carries a SINGLE-USE
    // token and no ?redirect=, so a silent redirect to the app leaves the user
    // on a dashboard and never says why their emailed link did not open the
    // recovery form -- and `replace` takes the tokened address out of that
    // tab's history too.
    <SignedOutOnly whenSignedIn="explain">
      <RecoverCompletePage />
    </SignedOutOnly>
  )
}
