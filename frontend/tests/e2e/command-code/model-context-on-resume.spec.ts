import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { expectNativeResumeContext } from '../helpers/nativeResume'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'

commandCodeTest('uses previous conversation context after the native session reopens', async ({ native }) => {
  const resumed = await exerciseSessionResume(native)
  const body = JSON.stringify(resumed.request.body)
  expect(body).toMatch(/RESUMEPROMPT[a-f0-9]+/)
  expect(body).toMatch(/RESUMEANSWER[a-f0-9]+/)
  expectNativeResumeContext(nativeModelConversationTurns(resumed.request), resumed)
})
