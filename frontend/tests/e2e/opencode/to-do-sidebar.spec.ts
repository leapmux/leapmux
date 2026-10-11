import { exerciseTodoListReplacement } from '../helpers/todoSidebar'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('the sidebar follows each list the agent writes, and keeps it after a reload', async ({ native }) => {
  await exerciseTodoListReplacement(native)
})
