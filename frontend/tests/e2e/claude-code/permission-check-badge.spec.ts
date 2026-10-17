import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { expectNoUnrenderedRow } from '../helpers/nativeConversation'
import { bashToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, permissionModeOffered, sendMessage, waitForAgentStarted, waitForSettingsHydrated } from '../helpers/ui'

/**
 * The permission-check badge while the Auto Mode classifier holds a tool call.
 *
 * A new Claude session starts in Auto Mode. The session menu drops that mode when the CLI refuses it. This spec
 * needs the mode and never skips, so it fails when the menu does not offer the mode.
 */
claudeTest('shows the permission-check badge while the Auto Mode classifier holds a tool call', async ({ page, authenticatedClaudeWorkspace, modelScript }) => {
  // The fixture opens a Claude session with no pinned permission mode, so the session asks for Auto Mode.
  void authenticatedClaudeWorkspace
  await waitForAgentStarted(page)
  await waitForSettingsHydrated(page)
  // Fail here, and never skip, when the session does not offer Auto Mode.
  expect(await permissionModeOffered(page, 'auto'), 'permissionModeOffered(page, \'auto\') is false').toBe(true)
  await expectSettingsChip(page, 'Auto Mode')

  const gate = 'classifier'
  // This rule matches the same request as the claude-auto-harm housekeeping rule. A newer rule of the same priority
  // matches first, so the classifier answer waits at the gate until the test releases it.
  await modelScript.rule({
    name: 'held-classifier',
    priority: 'high',
    when: {
      protocol: 'anthropic-messages',
      system: 'You are a security monitor for autonomous AI coding agents\\.',
      user: 'Respond with <severity>N</severity> ONLY\\.',
    },
    respond: { text: '<severity>0</severity>', gate },
  })
  const finalAnswer = 'The permission check passed.'
  // A command that the local fast path allows never reaches the classifier. A network command skips that fast path,
  // so Auto Mode gives its permission decision to the classifier.
  await modelScript.queue(
    { toolCalls: [bashToolCall(AgentProvider.CLAUDE_CODE, 'permission-check-badge', 'curl -s http://example.invalid')] },
    { text: finalAnswer },
  )
  try {
    await sendMessage(page, modelScript.prompt('Run the scripted shell command once, then reply.'))
    await modelScript.waitForGate(gate)
    const badge = page.locator('[data-testid="tool-running-badge"]:visible')
    // Claude Code reports the check after its own four-second timer. The default expect timeout covers that timer.
    await expect(badge).toHaveText('Checking permission…')
    await modelScript.releaseGate(gate)
    // The badge leaves when the tool call ends.
    await expect(badge).not.toBeVisible()
    // The follow-up model turn renders the final answer.
    await expect(assistantBubbles(page).filter({ hasText: finalAnswer })).not.toHaveCount(0)
    await expectNoUnrenderedRow(page)
  }
  finally {
    await modelScript.releaseGateIfHeld(gate)
  }
})
