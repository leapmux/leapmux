import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { deepseekHarnessModelContextText } from './modelContextText'
import { nativeContext } from './scenarios'

deepseekHarnessTest('keeps prior context in the actual native request after reopening the session', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  const request = await exerciseSessionResume(context)
  const text = deepseekHarnessModelContextText(request)
  expect(text).toContain('RESUMEPROMPT')
  expect(text).toContain('RESUMEANSWER')
})
