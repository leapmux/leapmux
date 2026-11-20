import type { CeremonyConfig } from '~/routes/(ceremonies)'
import { onMount } from 'solid-js'
import { RecoverCompletePage } from '~/components/common/RecoverCompletePage'
import { setPageTitle } from '~/lib/pageTitle'

export const ceremonyConfig: CeremonyConfig = {
  // The address carries a single-use recovery token. A silent bounce would
  // take the tokened address out of the tab's history and explain nothing,
  // so the gate names the conflict and leaves the token unused.
  gate: 'explain',
}

export default function RecoverAccountCompleteRoute() {
  onMount(() => {
    setPageTitle('Choose a new password')
  })
  return <RecoverCompletePage />
}
