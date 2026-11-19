import { onMount } from 'solid-js'
import { HubPage } from '~/components/common/HubPage'
import { RecoverCompletePage } from '~/components/common/RecoverCompletePage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { setPageTitle } from '~/lib/pageTitle'

export default function RecoverAccountCompleteRoute() {
  onMount(() => {
    setPageTitle('Choose a new password')
  })
  return (
    <SignedOutOnly whenSignedIn="explain">
      <HubPage>
        <RecoverCompletePage />
      </HubPage>
    </SignedOutOnly>
  )
}
