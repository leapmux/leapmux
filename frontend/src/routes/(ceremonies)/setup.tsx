import type { CeremonyConfig } from '~/routes/(ceremonies)'
import { onMount } from 'solid-js'
import { SetupPage } from '~/components/common/SetupPage'
import { setPageTitle } from '~/lib/pageTitle'

export const ceremonyConfig: CeremonyConfig = {
  gate: 'redirect',
}

export default function SetupRoute() {
  onMount(() => {
    setPageTitle('Setup')
  })
  return <SetupPage />
}
