import { copilotTest } from '../copilot-fixtures'
import { exerciseTodoListReplacement } from '../helpers/todoSidebar'
import { bypassToolRequests } from './scenarios'

copilotTest('the sidebar follows each checklist the agent writes, and keeps it after a reload', async ({ native }) => {
  await bypassToolRequests(native)
  await exerciseTodoListReplacement(native)
})
