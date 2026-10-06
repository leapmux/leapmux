import { commandCodeTest } from '../command-code-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

commandCodeTest('interrupts actual native model and tool turns without replacing the session', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await exerciseInterruptTurn(context)
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
