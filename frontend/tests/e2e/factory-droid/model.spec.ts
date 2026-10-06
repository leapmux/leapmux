import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { DROID_MOCK_MODEL_IDS, MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { expectDroidNativeSettings } from './settingsUpdates'

droidTest.describe('Factory Droid settings', () => {
  droidTest('sends a selected custom model to the mock and keeps it after reload', async ({ native }) => {
    await exerciseNativeOption(native, {
      groupId: 'model',
      value: DROID_MOCK_MODEL_IDS.alternate,
      nativeProof: async (request) => {
        expect(request.body).toMatchObject({ model: MOCK_MODELS.droidAlt })
        await expectDroidNativeSettings(native, { modelId: DROID_MOCK_MODEL_IDS.alternate })
      },
    })
  })
})
