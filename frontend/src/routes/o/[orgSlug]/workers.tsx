import { onMount } from 'solid-js'
import { WorkerListPage } from '~/components/workers/WorkerListPage'

export default function WorkersRoute() {
  onMount(() => {
    document.title = 'Workers - LeapMux'
  })
  return <WorkerListPage />
}
