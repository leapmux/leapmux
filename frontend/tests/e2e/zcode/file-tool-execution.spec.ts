import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { bashToolCall, editToolCall, readToolCall, zcodeReadRangeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, expectSettingsChip, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('renders an applied file edit', async ({ page, authenticatedZCodeWorkspace, modelScript }) => {
  void authenticatedZCodeWorkspace
  // Build mode asks before a write.
  // Select Yolo so the scripted write completes and the case can inspect its applied diff.
  await applyPermissionPreset(page, 'bypass')
  await expectSettingsChip(page, 'Yolo')
  // ZCode requires an earlier Read before Edit.
  // It otherwise returns "File has not been read yet. Read it first before writing to it."
  // The former seeded fixture did not exercise that native precondition.
  await modelScript.queue(
    { toolCalls: [bashToolCall(AgentProvider.ZCODE, 'seed-parity', 'printf "const parityBefore = 1\n" > parity.ts')] },
    { toolCalls: [readToolCall(AgentProvider.ZCODE, 'parity-read', 'parity.ts')] },
    { toolCalls: [editToolCall(AgentProvider.ZCODE, 'parity-edit', { path: 'parity.ts', before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
    { text: 'I changed parity.ts.' },
  )
  await sendMessage(page, modelScript.prompt('Change parity.ts.'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page, 180_000)

  const content = page.locator('[data-file-diff]:visible')
  await expect(content.filter({ hasText: 'const parityAfter = 2' }).first()).toBeVisible()
  await expect(content.filter({ hasText: 'const parityBefore = 1' }).first()).toBeVisible()
})

zcodeTest('reads and changes actual scratch bytes through native file tools', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseFileToolExecution(context, { prepare: () => applyPermissionPreset(page, 'bypass'), readAfterCall: (id, path) => zcodeReadRangeToolCall(id, path, { offset: 1, limit: 1 }) })
})
