import type { CeremonyConfig } from '~/routes/(ceremonies)'
import { onMount } from 'solid-js'
import { LoginPage } from '~/components/common/LoginPage'
import { BrowserSignIn } from '~/components/desktop/BrowserSignIn'
import { setPageTitle } from '~/lib/pageTitle'

export const ceremonyConfig: CeremonyConfig = {
  gate: 'redirect',
  // The in-app desktop sign-in. Without the slot, HubPage falls back to
  // ContinueInBrowser and the desktop app has no sign-in to offer.
  inDesktop: isCurrentArrival => <BrowserSignIn isCurrentArrival={isCurrentArrival} />,
}

export default function LoginRoute() {
  onMount(() => {
    setPageTitle('Login')
  })
  return <LoginPage />
}
