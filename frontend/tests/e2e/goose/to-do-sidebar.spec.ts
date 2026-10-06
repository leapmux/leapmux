import { gooseTest } from '../goose-fixtures'
import { exerciseGooseTodoListReplacement } from './todoScenario'

gooseTest('the sidebar follows each checklist the agent writes, and keeps it after a reload', async ({ native }) => {
  await exerciseGooseTodoListReplacement(native)
})
