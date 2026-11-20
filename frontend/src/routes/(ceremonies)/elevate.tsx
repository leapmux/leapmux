import type { CeremonyConfig } from '~/routes/(ceremonies)'
import { onMount } from 'solid-js'
import { ElevatePage } from '~/components/common/ElevatePage'
import { setPageTitle } from '~/lib/pageTitle'

export const ceremonyConfig: CeremonyConfig = {
  // A gate would bounce the signed-in visitor this step-up page serves.
  gate: false,
}

export default function ElevateRoute() {
  onMount(() => {
    setPageTitle('Verify your identity')
  })
  return <ElevatePage />
}
