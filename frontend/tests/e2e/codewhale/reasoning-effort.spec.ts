import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { CODEWHALE_VISION_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'

codewhaleTest.describe('Codewhale settings', () => {
  // The native catalog gives every tested route the same effort vocabulary.
  // It ignores reasoning=false and reasoning_effort="unsupported" in the private cache.
  // Thus this environment cannot offer a model that hides effort or lacks a selected level.
  // The runtime takes the effort per turn, so the model request is where the choice must arrive.
  codewhaleTest('sends the chosen effort with the next turn', async ({ native }) => {
    await exerciseNativeOption(native, {
      groupId: 'effort',
      value: 'high',
      nativeProof: request => expect(request.body).toHaveProperty('reasoning_effort', 'high'),
    })
  })
})

// The effort belongs to LeapMux, and the next turn sends it. Nothing in the runtime may reset it on a model switch.
codewhaleTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'high' },
    model: CODEWHALE_VISION_MODEL_ID,
    nativeProof: (request) => {
      expect(request.body).toHaveProperty('model', CODEWHALE_VISION_MODEL_ID)
      expect(request.body).toHaveProperty('reasoning_effort', 'high')
    },
  })
})
