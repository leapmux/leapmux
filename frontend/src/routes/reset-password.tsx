import { onMount } from 'solid-js'
import { ResetPasswordPage } from '~/components/common/ResetPasswordPage'
import { setPageTitle } from '~/lib/pageTitle'

export default function ResetPasswordRoute() {
  onMount(() => {
    setPageTitle('Reset password')
  })
  return <ResetPasswordPage />
}
