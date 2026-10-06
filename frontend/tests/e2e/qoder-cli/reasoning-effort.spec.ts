import { MOCK_MODELS, QODER_ALTERNATE_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { expect, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI effort and session goal', () => {
  qoderTest('uses the selected reasoning effort in the next native model request', async ({ native }) => {
    const effortChip = native.page.locator('[data-testid="composer-effort-trigger"]:visible')
    const prior = 'PRIOR_QODER_EFFORT_CONTEXT'
    await exerciseNativeOption(native, {
      groupId: 'effort',
      value: 'low',
      // The native session takes the effort flag after its first turn.
      prepare: async () => {
        await sendNativeAnswer(native, 'Remember the effort context marker.', prior)
      },
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ reasoning_effort: 'low' })
        expect(JSON.stringify(request.body)).toContain(prior)
      },
    })
    await expect(effortChip).toContainText('Low')
  })
})

qoderTest.describe('Qoder CLI model switch', () => {
  // The effort is a launch flag that does not depend on the model, so a model switch must keep it on screen
  // and in the Worker row, and it must restart nothing. The alternate mock model declares no reasoning, so
  // Qoder sends no effort for it. The native proof is the model, and the kept setting comes from the helper.
  // The native session takes the flag after its first turn, as the effort test above does.
  qoderTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
    await exerciseModelSwitchKeepsOption(native, {
      prepare: async () => {
        await sendNativeAnswer(native, 'Reply once before the effort changes.', 'The first turn answered.')
      },
      kept: { groupId: 'effort', value: 'low' },
      model: QODER_ALTERNATE_MODEL_ID,
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: MOCK_MODELS.qoder })
      },
    })
  })
})
