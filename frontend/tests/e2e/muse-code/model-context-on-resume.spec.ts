import { expect } from '@playwright/test'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { expectNativeResumeContext } from '../helpers/nativeResume'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'
import { museTest } from '../muse-fixtures'

museTest('sends the original exchange in order to the native model after a picker resume', async ({ native }) => {
  const resumed = await exerciseSessionResume(native)
  expect(resumed.originalRequest.mockCredential?.accepted).toBe(true)
  expect(resumed.request.mockCredential?.accepted).toBe(true)
  expect(resumed.request.protocol).toBe('openai-responses')
  expect(resumed.request.path).toBe('/v1/responses')
  expectNativeResumeContext(nativeModelConversationTurns(resumed.request), resumed)
})
