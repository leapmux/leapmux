import { onMount } from 'solid-js'
import { VerifyEmailPage } from '~/components/common/VerifyEmailPage'
import { setPageTitle } from '~/lib/pageTitle'

export default function VerifyEmailRoute() {
  onMount(() => {
    setPageTitle('Verify email')
  })
  return <VerifyEmailPage />
}
