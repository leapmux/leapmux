import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { expectNativeResumeContext } from '../helpers/nativeResume'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'
import { deepseekHarnessModelContextText } from './modelContextText'

deepseekHarnessTest('keeps prior context in the actual native request after reopening the session', async ({ native }) => {
  const resumed = await exerciseSessionResume(native)
  const text = deepseekHarnessModelContextText(resumed.request)
  expect(text).toContain('RESUMEPROMPT')
  expect(text).toContain('RESUMEANSWER')
  expectNativeResumeContext(nativeModelConversationTurns(resumed.request), resumed)
})
