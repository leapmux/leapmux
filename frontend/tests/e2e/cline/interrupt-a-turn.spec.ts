import { clineTest } from '../cline-fixtures'
import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT } from '../helpers/ui'
import { nativeContext } from './scenarios'

clineTest.describe('Cline interrupt', () => {
  clineTest('stops a model call and continues the session at the next prompt', async ({ native }) => {
    await exerciseInterruptTurn(native, { kind: 'model', prompt: 'Write a long essay about the history of computing.', divider: /^Turn interrupted/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT, contextMarkers: ['history of computing'] } })
  })

  clineTest('stops a running command and continues the session at the next prompt', async ({ native }) => {
    await exerciseInterruptTurn(native, { kind: 'tool', prompt: 'Wait for ten minutes.', divider: /^Turn interrupted/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT, contextMarkers: ['Wait for ten minutes.'] } })
  })

  clineTest('keeps the interrupted partial answer and its marker after reload', async ({ native }) => {
    await exerciseInterruptedPartialAnswer(native)
  })

  // The question opens in LeapMux's default Act mode, as in the control-request spec. Auto-approve answers each tool.
  clineTest('withdraws a waiting question and continues the session at the next prompt', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId })
    await exerciseControlInterrupt(context, { control: 'question' })
  })
})
