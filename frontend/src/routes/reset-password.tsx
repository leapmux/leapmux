import { onMount } from 'solid-js'
import { ResetPasswordPage } from '~/components/common/ResetPasswordPage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { setPageTitle } from '~/lib/pageTitle'

export default function ResetPasswordRoute() {
  onMount(() => {
    setPageTitle('Reset password')
  })
  return (
    // explain, not the default redirect: this address carries a SINGLE-USE
    // token and no ?redirect=, so a silent bounce to the app leaves the user
    // on a dashboard with no idea why their emailed link went nowhere -- and
    // `replace` takes the tokened address out of that tab's history too.
    <SignedOutOnly whenSignedIn="explain">
      <ResetPasswordPage />
    </SignedOutOnly>
  )
}
