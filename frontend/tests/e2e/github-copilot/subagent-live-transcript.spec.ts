import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { COPILOT_E2E_SKIP_REASON, copilotTest } from '../copilot-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { applyPermissionPreset, assistantBubbles, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest('shows a child tool before the child finishes', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  void authenticatedCopilotWorkspace
  await applyPermissionPreset(page, 'bypass')
  await waitForSettingsIdle(page)
  await expectNoRegistryRows(page, leapmuxServer)
  const row = await withCleanup(async () => {
    await modelScript.rule(
      {
        name: 'the child runs a shell probe',
        when: { user: 'Run printf copilot-child-live' },
        respond: { toolCalls: [bashToolCall(AgentProvider.GITHUB_COPILOT, 'child-shell', 'printf copilot-child-live')] },
        once: true,
      },
      {
        name: 'the child answers after its shell probe',
        when: { user: 'Run printf copilot-child-live' },
        respond: { text: 'COPILOT_CHILD_LIVE_DONE', gate: 'copilot-child-final' },
      },
    )
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(AgentProvider.GITHUB_COPILOT, 'spawn-copilot-live', {
        description: 'Run the child shell probe',
        prompt: modelScript.prompt('Run printf copilot-child-live, then report the result.'),
      })] },
      { text: 'COPILOT_ROOT_LIVE_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one child to run the shell probe.'))
    await modelScript.waitForGate('copilot-child-final')
    const row = await requireRegistryRow(page)

    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'printf copilot-child-live' }).first()).toBeVisible()
    await expect(row).not.toHaveAttribute('data-status', 'completed')

    return row
  }, async () => {
    await modelScript.releaseGateIfHeld('copilot-child-final')
  })
  await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  await expectRowBecomesFinal(page, row)
  await expect(assistantBubbles(page).filter({ hasText: 'COPILOT_CHILD_LIVE_DONE' }).first()).toBeVisible()
})
