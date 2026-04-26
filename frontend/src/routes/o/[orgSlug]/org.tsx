import { onMount } from 'solid-js'
import { OrgManagementPage } from '~/components/org/OrgManagementPage'
import { setPageTitle } from '~/lib/pageTitle'

export default function OrgManagementRoute() {
  onMount(() => {
    setPageTitle('Organization')
  })
  return <OrgManagementPage />
}
