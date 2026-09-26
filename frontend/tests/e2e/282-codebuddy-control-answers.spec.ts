import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import { bashToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, sendMessage, waitForAgentIdle, waitForControlBanner } from './helpers/ui'

/**
 * 282 — CodeBuddy Code control answers.
 *
 * The agent opens in Default, which raises a banner for each tool call. Typing
 * before the deny turns the button into "Send feedback"; the typed text becomes
 * the denial reason and the worker forwards it as CodeBuddy's own `reason`
 * field. The working directory proves the refused call never ran.
 */
codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.CODEBUDDY

/** The request body of every answer the script gave after the first, joined. */
function laterRequests(status: { requests: { stepIndex?: number, body: unknown }[] }, from: number): string {
  return status.requests
    .filter(request => (request.stepIndex ?? -1) >= from)
    .map(request => JSON.stringify(request.body))
    .join('\n')
}

codebuddyTest.describe('CodeBuddy Code control answers', () => {
  codebuddyTest('a denied command does not run, and the reason reaches the model', async ({ askingCodebuddyWorkspace, page, modelScript }) => {
    const { workingDir } = askingCodebuddyWorkspace
    const marker = join(workingDir, 'codebuddy-denied-marker')
    await modelScript.queue(
      { toolCalls: [bashToolCall(PROVIDER, 'deny-call', 'touch codebuddy-denied-marker')] },
      { text: 'I stopped at the denial.' },
    )
    await sendMessage(page, modelScript.prompt('Create the marker file.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('touch codebuddy-denied-marker')
    // Typing turns the deny button into "Send feedback", which sends the typed
    // text as the denial reason.
    await page.locator('[data-testid="composer-editor"] .ProseMirror').click()
    await page.keyboard.type('the probe is not wanted here', { delay: 50 })
    const deny = page.getByTestId('control-deny-btn')
    await expect(deny).toHaveText('Send feedback')
    await deny.click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    expect(existsSync(marker), 'the denied command never ran').toBe(false)
    expect(laterRequests(status, 1)).toContain('the probe is not wanted here')
    await expect(assistantBubbles(page).filter({ hasText: 'I stopped at the denial.' }).first()).toBeVisible()
  })
})
