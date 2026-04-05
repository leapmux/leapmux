import { onMount } from 'solid-js'
import { SetupPage } from '~/components/common/SetupPage'

export default function SetupRoute() {
  onMount(() => {
    document.title = 'Setup - LeapMux'
  })
  return <SetupPage />
}
