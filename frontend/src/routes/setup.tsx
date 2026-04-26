import { onMount } from 'solid-js'
import { SetupPage } from '~/components/common/SetupPage'
import { setPageTitle } from '~/lib/pageTitle'

export default function SetupRoute() {
  onMount(() => {
    setPageTitle('Setup')
  })
  return <SetupPage />
}
