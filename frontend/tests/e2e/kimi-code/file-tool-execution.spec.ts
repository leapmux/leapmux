import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, assistantBubbles, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { KIMI_E2E_SKIP_REASON, kimiTest, occurrences, stepRequestBody } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

const KIMI = AgentProvider.KIMI_CODE

kimiTest.describe('uses Kimi Code tools', () => {
  // Never Ask skips native tool approvals. The permissions spec tests those approvals.
  kimiTest.beforeEach(async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
  })

  kimiTest('an edit renders its diff, and a read renders the file', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    const path = join(createNativeToolDirectory(authenticatedKimiWorkspace.workingDir), 'parity.ts')
    await modelScript.queue(
      { toolCalls: [writeToolCall(KIMI, 'seed-file', { path, content: 'const parityBefore = 1\n' })] },
      { toolCalls: [editToolCall(KIMI, 'parity-edit', { path, before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
      { toolCalls: [readToolCall(KIMI, 'parity-read', path)] },
      { text: 'I changed parity.ts and read it back.' },
    )
    await sendMessage(page, modelScript.prompt('Create parity.ts, change it, and read it back.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The write's diff holds the old line too, so the EDIT's diff must hold both.
    const editDiff = page.locator('[data-file-diff]:visible').filter({ hasText: 'const parityAfter = 2' }).first()
    await expect(editDiff).toBeVisible()
    await expect(editDiff).toContainText('const parityBefore = 1')
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'parity.ts' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I changed parity.ts and read it back.' })).not.toHaveCount(0)

    // The edit's own arguments state the new line in every later request. So the
    // request after the read states it more often than the request before the
    // read only when the READ's result holds it.
    const { requests } = await modelScript.status()
    expect(occurrences(stepRequestBody(requests, 3), 'const parityAfter = 2'), 'the read returned the edited file')
      .toBeGreaterThan(occurrences(stepRequestBody(requests, 2), 'const parityAfter = 2'))
  })
})
