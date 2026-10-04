import { existsSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, openWorkspace, sendMessage, userBubbles, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

import { openQwenAgent, QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.QWEN_CODE

qwenTest.describe('Qwen Code control requests', () => {
  // Qwen's `default` mode asks before a shell command. The reply contains one of the offered options.
  // The native reply has no reason field. The reader's reason follows in a separate message.
  qwenTest('approves one command and rejects the next with a reason', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Default')
    const approved = join(workingDir, 'approved.txt')
    const rejected = join(workingDir, 'rejected.txt')

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'qwen-approved', `touch ${approved}`)] },
      { toolCalls: [bashToolCall(PROVIDER, 'qwen-rejected', `touch ${rejected}`)] },
    )
    await sendMessage(page, modelScript.prompt('Create the two scripted files.'))
    await modelScript.waitForSteps(1)
    const banner = visibleControlBanner(page)
    await expect(banner).toContainText(`touch ${approved}`)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps(2)
    await expect(banner).toContainText(`touch ${rejected}`)
    expect(existsSync(approved)).toBe(true)
    // The reason ends the rejected turn and opens the next one, which answers it.
    await modelScript.queue({ text: 'I will not create the second file.' })
    const editor = page.getByTestId('composer-editor').locator('.ProseMirror')
    await editor.fill('Do not create the second file.')
    await page.keyboard.press('Meta+Enter')
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(userBubbles(page).filter({ hasText: 'Do not create the second file.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I will not create the second file.' })).toBeVisible()
    expect(existsSync(rejected)).toBe(false)
  })
})
