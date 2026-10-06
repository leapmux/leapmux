import { join } from 'node:path'
import { expect } from '@playwright/test'
import { createNativeToolDirectory } from '../helpers/nativeToolDirectory'
import { expectFileDiff, nativeFileReadResult, PARITY_AFTER, PARITY_BEFORE, runNativeToolSteps } from '../helpers/nativeToolExecution'
import { editToolCall, readToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, assistantBubbles, expectSettingsChip, toolRows, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('uses Kimi Code tools', () => {
  // Never Ask skips native tool approvals. The permissions spec tests those approvals.
  kimiTest.beforeEach(async ({ native }) => {
    await waitForSettingsHydrated(native.page)
    await applyPermissionPreset(native.page, 'bypass')
    await expectSettingsChip(native.page, 'Never Ask')
  })

  kimiTest('an edit renders its diff, and a read renders the file', async ({ authenticatedKimiWorkspace, native }) => {
    const { page, modelScript } = native
    const path = join(createNativeToolDirectory(authenticatedKimiWorkspace.workingDir), 'parity.ts')
    const start = await runNativeToolSteps(native, {
      steps: [
        { toolCalls: [writeToolCall(native.provider, 'seed-file', { path, content: `${PARITY_BEFORE}\n` })] },
        { toolCalls: [editToolCall(native.provider, 'parity-edit', { path, before: PARITY_BEFORE, after: PARITY_AFTER })] },
        { toolCalls: [readToolCall(native.provider, 'parity-read', path)] },
      ],
      prompt: 'Create parity.ts, change it, and read it back.',
      answer: 'I changed parity.ts and read it back.',
      permissions: 'none',
    })

    // The write's diff holds the old line too, so the EDIT's diff must hold both.
    await expectFileDiff(page, { before: PARITY_BEFORE, after: PARITY_AFTER })
    await expect(toolRows(page).filter({ hasText: 'parity.ts' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I changed parity.ts and read it back.' })).not.toHaveCount(0)

    // The edit's own arguments state both lines in every later request, so only
    // the read's own result can prove what the read returned. The read runs after
    // the edit, so its result holds the new line and not the old line.
    await nativeFileReadResult(await modelScript.requestAt(start + 3), 'parity-read', PARITY_AFTER, PARITY_BEFORE)
  })
})
