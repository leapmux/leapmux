import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { exerciseFileToolExecution, waitForNativeToolSteps } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { copilotApplyPatchToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, sendMessage } from '../helpers/ui'

copilotTest('runs native freeform patches and keeps the actual file diff after reload', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  await exerciseFileToolExecution(context, {
    prepare: () => applyPermissionPreset(page, 'bypass'),
    writeCall: (id, path, content) => copilotApplyPatchToolCall(id, `*** Begin Patch\n*** Add File: ${path}\n${content.replace(/\n$/, '').split('\n').map(line => `+${line}`).join('\n')}\n*** End Patch\n`),
    editCall: (id, path, before, after) => copilotApplyPatchToolCall(id, `*** Begin Patch\n*** Update File: ${path}\n@@\n-${before}\n+${after}\n*** End Patch\n`),
  })
})

copilotTest('keeps a scratch file unchanged when its native patch target is absent', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
  const directory = authenticatedCopilotWorkspace.workingDir
  if (!directory)
    throw new Error('The Copilot patch proof requires a working directory.')
  const file = join(directory, 'native-invalid-patch.txt')
  const original = 'Keep these actual file bytes.\n'
  writeFileSync(file, original)
  await applyPermissionPreset(page, 'bypass')
  await modelScript.queue(
    { toolCalls: [copilotApplyPatchToolCall('invalid-native-patch', `*** Begin Patch\n*** Update File: ${file}\n@@\n-ABSENT_PATCH_TARGET\n+MUST_NOT_BE_WRITTEN\n*** End Patch\n`)] },
    { text: 'The native invalid patch turn ended.' },
  )
  await sendMessage(page, modelScript.prompt('Apply the scripted patch and report its native result.'))
  await waitForNativeToolSteps({ page, modelScript, provider: AgentProvider.GITHUB_COPILOT }, 2)
  expect(readFileSync(file, 'utf8')).toBe(original)
  const status = await modelScript.status()
  const result = nativeToolResult(status.requests.find(request => request.stepIndex === 1), 'invalid-native-patch')
  expect(result).toMatch(/find|match|patch|target/i)
  expect(result).not.toContain('Updated 1 file')
  const failed = page.locator('[data-tool-status="failed"]:visible').filter({ hasText: 'native-invalid-patch.txt' }).first()
  await expect(failed).toBeVisible()
})
