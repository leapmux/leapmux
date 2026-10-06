import { gooseTest } from '../goose-fixtures'
import { exerciseGooseMcpForm } from './mcpScenario'
import { exerciseGoosePermissionRemoval } from './permissionScenario'
import { exerciseGooseTodoListReplacement } from './todoScenario'

gooseTest('permission-prompts: the sidebar follows each checklist the agent writes, and keeps it after a reload', async ({ native }) => {
  await exerciseGooseTodoListReplacement(native)
})

gooseTest('permission-prompts: roundtrips zero, false, and blue through native form elicitation', async ({ native }) => {
  await exerciseGooseMcpForm(native)
})

gooseTest('permission-prompts: smart mode asks before a removal and auto mode runs it', async ({ native }) => {
  await exerciseGoosePermissionRemoval(native)
})
