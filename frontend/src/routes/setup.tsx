import { onMount } from 'solid-js'
import { HubPage } from '~/components/common/HubPage'
import { SetupPage } from '~/components/common/SetupPage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { setPageTitle } from '~/lib/pageTitle'

export default function SetupRoute() {
  onMount(() => {
    setPageTitle('Setup')
  })
  return (
    <SignedOutOnly>
      <HubPage>
        <SetupPage />
      </HubPage>
    </SignedOutOnly>
  )
}
