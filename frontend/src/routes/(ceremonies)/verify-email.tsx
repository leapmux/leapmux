import type { CeremonyConfig } from '~/routes/(ceremonies)'
import { useNavigate, useSearchParams } from '@solidjs/router'
import { onMount } from 'solid-js'
import { VerifyEmailPage } from '~/components/common/VerifyEmailPage'
import { ContinueInBrowser } from '~/components/desktop/ContinueInBrowser'
import { useAuth } from '~/context/AuthContext'
import { openHubPage } from '~/lib/hubSite'
import { setPageTitle } from '~/lib/pageTitle'
import { postVerificationDestination } from '~/lib/postAuthNavigate'
import { stringParam } from '~/lib/searchParam'

function DesktopVerifyEmail(props: { isCurrentArrival: () => boolean }) {
  const auth = useAuth()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const check = async () => {
    if (!await auth.reloadUser())
      throw new Error('The account changed while LeapMux checked it. Try again.')
    if (!auth.user()?.emailVerified)
      throw new Error('Your email is still waiting for verification.')
    if (!props.isCurrentArrival())
      return
    const destination = postVerificationDestination(stringParam(searchParams.redirect))
    if (destination.kind === 'document') {
      await openHubPage(destination.path)
      navigate('/', { replace: true })
      return
    }
    navigate(destination.path, { replace: true })
  }
  return (
    <ContinueInBrowser onCheck={check} />
  )
}

export const ceremonyConfig: CeremonyConfig = {
  // A gate would bounce the signed-in visitor this page serves mid-flow.
  gate: false,
  inDesktop: isCurrentArrival => <DesktopVerifyEmail isCurrentArrival={isCurrentArrival} />,
}

export default function VerifyEmailRoute() {
  onMount(() => {
    setPageTitle('Verify email')
  })
  return <VerifyEmailPage />
}
