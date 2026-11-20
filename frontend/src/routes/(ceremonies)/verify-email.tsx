import type { CeremonyConfig } from '~/routes/(ceremonies)'
import { onMount } from 'solid-js'
import { VerifyEmailPage } from '~/components/common/VerifyEmailPage'
import { setPageTitle } from '~/lib/pageTitle'

export const ceremonyConfig: CeremonyConfig = {
  // A gate would bounce the signed-in visitor this page serves mid-flow.
  gate: false,
}

export default function VerifyEmailRoute() {
  onMount(() => {
    setPageTitle('Verify email')
  })
  return <VerifyEmailPage />
}
