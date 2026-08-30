import { onMount } from 'solid-js'
import { RecoverPage } from '~/components/common/RecoverPage'
import { SignedOutOnly } from '~/components/common/SignedOutOnly'
import { setPageTitle } from '~/lib/pageTitle'

export default function RecoverAccountRoute() {
  onMount(() => {
    setPageTitle('Recover your account')
  })
  return (
    <SignedOutOnly>
      <RecoverPage />
    </SignedOutOnly>
  )
}
