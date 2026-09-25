import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, editToolCall, readToolCall, writeToolCall } from './helpers/providerToolCalls'
import { applyPermissionPreset, assistantBubbles, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from './helpers/ui'
import { expect, KIMI_E2E_SKIP_REASON, kimiTest, occurrences, stepRequestBody } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

const KIMI = AgentProvider.KIMI_CODE

kimiTest.describe('uses Kimi Code tools', () => {
  // Always Ask stops every command and edit at a banner. These cases assert
  // what a tool DRAWS, so Never Ask takes the banner out of the way; the
  // control-request spec covers the banner itself.
  //
  // Each command prints a number that its own text does not state, such as
  // `kimi-test-output-42` from `kimi-test-output-$((40 + 2))`. The row's header
  // shows the command, so a marker that the command text holds matches the row
  // whether or not the command ran.
  kimiTest.beforeEach(async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
  })

  kimiTest('a command renders as a tool card with its output, and keeps it after a reload', async ({ page, modelScript }) => {
    await modelScript.queue(
      { toolCalls: [bashToolCall(KIMI, 'echo-call', 'echo "kimi-test-output-$((40 + 2))"')] },
      { text: 'The command printed its output.' },
    )
    await sendMessage(page, modelScript.prompt('Run the echo command and report the output.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const toolMessages = page.locator('[data-tool-message]:visible')
    await expect(toolMessages.filter({ hasText: 'kimi-test-output-42' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'The command printed its output.' })).not.toHaveCount(0)

    await page.reload()
    await expect(toolMessages.filter({ hasText: 'kimi-test-output-42' }).first()).toBeVisible()
  })

  // Kimi Code states a failed command's exit code only as a trailer on the
  // output, and the plugin reads it from there.
  kimiTest('a failed command shows its output and its exit code', async ({ page, modelScript }) => {
    await modelScript.queue(
      { toolCalls: [bashToolCall(KIMI, 'exit-call', `sh -c 'echo "hello-from-kimi-$((40 + 2))"; exit 7'`)] },
      { text: 'The command exited with status 7.' },
    )
    await sendMessage(page, modelScript.prompt('Run this exact command and report its result.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const toolMessages = page.locator('[data-tool-message]:visible')
    await expect(toolMessages.filter({ hasText: 'hello-from-kimi-42' }).first()).toBeVisible()
    await expect(toolMessages.filter({ hasText: 'Error (exit 7)' }).first()).toBeVisible()
    // The trailer is a fact the card states, not output it repeats.
    await expect(toolMessages.filter({ hasText: 'Command failed with exit code' })).toHaveCount(0)
  })

  kimiTest('an edit renders its diff, and a read renders the file', async ({ page, modelScript }) => {
    await modelScript.queue(
      { toolCalls: [writeToolCall(KIMI, 'seed-file', { path: 'parity.ts', content: 'const parityBefore = 1\n' })] },
      { toolCalls: [editToolCall(KIMI, 'parity-edit', { path: 'parity.ts', before: 'const parityBefore = 1', after: 'const parityAfter = 2' })] },
      { toolCalls: [readToolCall(KIMI, 'parity-read', 'parity.ts')] },
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
