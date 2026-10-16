import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { expectSettingsChip } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'
import { nativeContext } from './scenarios'

kiroTest.describe('interrupt for Kiro', () => {
  kiroTest('interrupts a running turn', async ({ native }) => {
    await exerciseInterruptTurn(native, { kind: 'model', prompt: 'Write a long report.', divider: /^Turn interrupted$/ })
  })

  kiroTest('interrupts a running tool', async ({ native }) => {
    await exerciseInterruptTurn(native, { kind: 'tool' })
  })

  // Kiro offers its question tool in a spec mode alone, as the agent-questions spec states, so the agent opens in Spec.
  kiroTest('withdraws a waiting question', async ({ page, askingKiroWorkspace, leapmuxServer, modelScript }) => {
    await expectSettingsChip(page, 'Spec')
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingKiroWorkspace.workspaceId })
    await exerciseControlInterrupt(context, { control: 'question' })
  })
})

kiroTest('keeps interrupted partial text and its marker after reload', async ({ native }) => {
  await exerciseInterruptedPartialAnswer(native)
})
