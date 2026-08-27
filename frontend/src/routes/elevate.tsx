import { onMount } from 'solid-js'
import { ElevatePage } from '~/components/common/ElevatePage'
import { setPageTitle } from '~/lib/pageTitle'

export default function ElevateRoute() {
  onMount(() => {
    setPageTitle('Verify your identity')
  })
  return <ElevatePage />
}
