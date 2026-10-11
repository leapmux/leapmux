import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { expectSettingsChip, openWorkspace } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { KIRO_AGENT, nativeContext } from './scenarios'

kiroTest.describe('Kiro interrupt', () => {
  kiroTest('interrupts a running turn', async ({ native }) => {
    await exerciseInterruptTurn(native, { kind: 'model', prompt: 'Write a long report.', divider: /^Turn interrupted$/ })
  })

  kiroTest('interrupts a running tool', async ({ native }) => {
    await exerciseInterruptTurn(native, { kind: 'tool' })
  })

  // Kiro offers its question tool in a spec mode alone, as the agent-questions spec states, so the agent opens in Spec.
  kiroTest('withdraws a waiting question', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'spec' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Spec')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
    await exerciseControlInterrupt(context, { control: 'question' })
  })
})
