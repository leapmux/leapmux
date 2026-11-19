import { onMount } from 'solid-js'
import { ElevatePage } from '~/components/common/ElevatePage'
import { HubPage } from '~/components/common/HubPage'
import { setPageTitle } from '~/lib/pageTitle'

export default function ElevateRoute() {
  onMount(() => {
    setPageTitle('Verify your identity')
  })
  return (
    <HubPage>
      <ElevatePage />
    </HubPage>
  )
}
