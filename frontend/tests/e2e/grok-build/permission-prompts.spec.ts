import { existsSync } from 'node:fs'

import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from '../grok-fixtures'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsOptionChosen, messageBubbles, openWorkspace, sendMessage, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.GROK_BUILD

grokTest.describe('Grok Build control requests', () => {
  // Grok's `ask` mode asks before a shell command writes a file. It permits `touch` and `mkdir`, so these commands redirect output.
  // An empty rejection ends the turn. A rejection with a reason puts that reason in native `followup_message`.
  // The same turn then continues.
  grokTest('approves one command and rejects the next with a reason the turn reads', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    const { workingDir } = await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId)
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsOptionChosen(page, 'approvalMode-ask')
    const approved = join(workingDir, 'approved.txt')
    const rejected = join(workingDir, 'rejected.txt')

    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'grok-approved', `printf approved > ${approved}`)] },
      { toolCalls: [bashToolCall(PROVIDER, 'grok-rejected', `printf rejected > ${rejected}`)] },
      { text: 'I read the reason and stopped.' },
    )
    await sendMessage(page, modelScript.prompt('Create the two scripted files.'))
    await modelScript.waitForSteps(1)
    const banner = visibleControlBanner(page)
    await expect(banner).toContainText(`printf approved > ${approved}`)
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()

    await modelScript.waitForSteps(2)
    await expect(banner).toContainText(`printf rejected > ${rejected}`)
    expect(existsSync(approved)).toBe(true)
    const editor = page.getByTestId('composer-editor').locator('.ProseMirror')
    await editor.fill('Do not create the second file.')
    await page.keyboard.press('Meta+Enter')
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    // The reason reached the model inside the same turn, and the saved answer
    // shows it rather than the option's words.
    const status = await modelScript.status()
    expect(JSON.stringify(status.requests.at(-1)?.body)).toContain('Do not create the second file.')
    await expect(messageBubbles(page).filter({ hasText: 'Sent feedback:' }).filter({ hasText: 'Do not create the second file.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'I read the reason and stopped.' })).toBeVisible()
    expect(existsSync(rejected)).toBe(false)
  })
})
