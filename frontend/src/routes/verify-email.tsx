import { onMount } from 'solid-js'
import { HubPage } from '~/components/common/HubPage'
import { VerifyEmailPage } from '~/components/common/VerifyEmailPage'
import { setPageTitle } from '~/lib/pageTitle'

export default function VerifyEmailRoute() {
  onMount(() => {
    setPageTitle('Verify email')
  })
  return (
    <HubPage>
      <VerifyEmailPage />
    </HubPage>
  )
}
