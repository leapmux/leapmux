import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

deepseekHarnessTest('cancels native model and command turns without replacing the session', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  await exerciseInterruptTurn(context)
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
