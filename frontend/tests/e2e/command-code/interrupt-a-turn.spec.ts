import { commandCodeTest } from '../command-code-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

commandCodeTest('interrupts actual native model and tool turns without replacing the session', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  await exerciseInterruptTurn(context)
  await exerciseInterruptTurn(context, { kind: 'tool' })
})
