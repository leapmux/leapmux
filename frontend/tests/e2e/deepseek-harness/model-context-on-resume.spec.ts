import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { expectNativeResumeContext } from '../helpers/nativeResume'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'
import { deepseekHarnessModelContextText } from './modelContextText'
import { nativeContext } from './scenarios'

deepseekHarnessTest('keeps prior context in the actual native request after reopening the session', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  const resumed = await exerciseSessionResume(context)
  const text = deepseekHarnessModelContextText(resumed.request)
  expect(text).toContain('RESUMEPROMPT')
  expect(text).toContain('RESUMEANSWER')
  expectNativeResumeContext(nativeModelConversationTurns(resumed.request), resumed)
})
