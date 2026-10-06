import { copilotTest } from '../copilot-fixtures'
import { exerciseTodoListReplacement } from '../helpers/todoSidebar'

copilotTest('the sidebar follows each checklist the agent writes, and keeps it after a reload', async ({ native }) => {
  await exerciseTodoListReplacement(native, { steps: ['Inspect the repository', 'Report their purpose'] })
})
