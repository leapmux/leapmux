import { onMount } from 'solid-js'
import { OrgManagementPage } from '~/components/org/OrgManagementPage'

export default function OrgManagementRoute() {
  onMount(() => {
    document.title = 'Organization - LeapMux'
  })
  return <OrgManagementPage />
}
