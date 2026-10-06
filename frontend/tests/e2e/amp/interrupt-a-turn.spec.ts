import { ampTest } from '../amp-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT } from '../helpers/ui'

ampTest.describe('Amp interrupt', () => {
  ampTest('stops a model call and continues the thread at the next prompt', async ({ native }) => {
    await exerciseInterruptTurn(native, { kind: 'model', prompt: 'Write a long essay about the history of computing.', divider: /^Turn interrupted/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT, contextMarkers: ['history of computing'] } })
  })

  ampTest('stops a running command and continues the thread at the next prompt', async ({ native }) => {
    await exerciseInterruptTurn(native, { kind: 'tool', prompt: 'Wait for ten minutes.', divider: /^Turn interrupted/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT, contextMarkers: ['Wait for ten minutes.'] } })
  })
})
