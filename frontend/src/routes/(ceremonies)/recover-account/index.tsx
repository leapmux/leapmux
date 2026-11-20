import type { CeremonyConfig } from '~/routes/(ceremonies)'
import { onMount } from 'solid-js'
import { RecoverPage } from '~/components/common/RecoverPage'
import { setPageTitle } from '~/lib/pageTitle'

export const ceremonyConfig: CeremonyConfig = {
  gate: 'redirect',
}

export default function RecoverAccountRoute() {
  onMount(() => {
    setPageTitle('Recover your account')
  })
  return <RecoverPage />
}
