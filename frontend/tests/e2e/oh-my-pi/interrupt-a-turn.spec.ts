import { exerciseControlInterrupt } from '../helpers/controlInterrupt'
import { exerciseInterruptedPartialAnswer, exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest.describe('Oh My Pi interrupt', () => {
  ohMyPiTest('stops a model call and takes the next prompt', async ({ native }) => {
    await exerciseInterruptTurn(native, { kind: 'model', prompt: 'Write a long essay about the history of computing.', divider: /^Turn interrupted \(.+\)$/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT } })
  })

  ohMyPiTest('stops a running command and takes the next prompt', async ({ native }) => {
    await exerciseInterruptTurn(native, { kind: 'tool', prompt: 'Wait for ten minutes.', divider: /^Turn interrupted \(.+\)1 tool$/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT } })
  })

  ohMyPiTest('keeps the interrupted partial answer and its marker after reload', async ({ native }) => {
    await exerciseInterruptedPartialAnswer(native)
  })

  // Oh My Pi's ask tool sends a series of native dialogs, which the Worker joins into one question request.
  ohMyPiTest('withdraws a waiting question and takes the next prompt', async ({ native }) => {
    await exerciseControlInterrupt(native, { control: 'question' })
  })
})
