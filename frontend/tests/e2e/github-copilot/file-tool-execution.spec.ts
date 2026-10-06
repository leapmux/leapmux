import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { exerciseFileToolExecution, runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { editToolCall } from '../helpers/providerToolCalls'
import { bypassToolRequests } from './scenarios'

copilotTest('runs native freeform patches and keeps the actual file diff after reload', async ({ native }) => {
  // `writeToolCall` and `editToolCall` (`helpers/providerToolCalls.ts`) build Copilot's freeform apply_patch calls.
  await exerciseFileToolExecution(native, { prepare: () => bypassToolRequests(native) })
})

copilotTest('keeps a scratch file unchanged when its native patch target is absent', async ({ authenticatedCopilotWorkspace, native }) => {
  const directory = authenticatedCopilotWorkspace.workingDir
  if (!directory)
    throw new Error('The Copilot patch proof requires a working directory.')
  const file = join(directory, 'native-invalid-patch.txt')
  const original = 'Keep these actual file bytes.\n'
  writeFileSync(file, original)
  await bypassToolRequests(native)
  const { resultRequest } = await runNativeToolTurn(native, {
    toolCalls: [editToolCall(native.provider, 'invalid-native-patch', { path: file, before: 'ABSENT_PATCH_TARGET', after: 'MUST_NOT_BE_WRITTEN' })],
    prompt: 'Apply the scripted patch and report its native result.',
    answer: 'The native invalid patch turn ended.',
  })
  expect(readFileSync(file, 'utf8')).toBe(original)
  const result = nativeToolResult(resultRequest, 'invalid-native-patch')
  expect(result).toMatch(/find|match|patch|target/i)
  expect(result).not.toContain('Updated 1 file')
  const failed = native.page.locator('[data-tool-status="failed"]:visible').filter({ hasText: 'native-invalid-patch.txt' }).first()
  await expect(failed).toBeVisible()
})
