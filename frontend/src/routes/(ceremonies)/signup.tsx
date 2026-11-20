import type { CeremonyConfig } from '~/routes/(ceremonies)'
import { onMount } from 'solid-js'
import { SignupPage } from '~/components/common/SignupPage'
import { setPageTitle } from '~/lib/pageTitle'

export const ceremonyConfig: CeremonyConfig = {
  gate: 'redirect',
}

export default function SignupRoute() {
  onMount(() => setPageTitle('Sign Up'))
  return <SignupPage />
}
