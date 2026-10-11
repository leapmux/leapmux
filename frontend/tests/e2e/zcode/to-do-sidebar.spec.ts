import { exerciseTodoListReplacement } from '../helpers/todoSidebar'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('the sidebar follows each list the agent writes, and keeps it after a reload', async ({ native }) => {
  await exerciseTodoListReplacement(native)
})
