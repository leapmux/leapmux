import { piTest } from '../pi-fixtures'
import { exercisePiGoalLifecycle, exercisePiGoalPanel } from './goalScenario'
import { nativeContext } from './scenarios'

piTest('pauses and resumes a native goal after reload', async ({ native }) => {
  await exercisePiGoalLifecycle(native)
})

piTest('controls a real Pi goal through the shared goal panel and confirmation', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  await exercisePiGoalPanel(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId }))
})
