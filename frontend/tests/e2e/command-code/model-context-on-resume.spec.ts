import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

commandCodeTest('uses previous conversation context after the native session reopens', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  const request = await exerciseSessionResume(context)
  const body = JSON.stringify(request.body)
  expect(body).toMatch(/RESUMEPROMPT[a-f0-9]+/)
  expect(body).toMatch(/RESUMEANSWER[a-f0-9]+/)
})
