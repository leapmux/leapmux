import { exerciseTodoListReplacement } from '../helpers/todoSidebar'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('the sidebar follows each list the agent writes, and keeps it after a reload', async ({ native }) => {
  await exerciseTodoListReplacement(native)
})
