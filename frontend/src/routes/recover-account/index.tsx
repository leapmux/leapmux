import { onMount } from 'solid-js'
import { HubPage } from '~/components/common/HubPage'
import { RecoverPage } from '~/components/common/RecoverPage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { setPageTitle } from '~/lib/pageTitle'

export default function RecoverAccountRoute() {
  onMount(() => {
    setPageTitle('Recover your account')
  })
  return (
    <SignedOutOnly>
      <HubPage>
        <RecoverPage />
      </HubPage>
    </SignedOutOnly>
  )
}
