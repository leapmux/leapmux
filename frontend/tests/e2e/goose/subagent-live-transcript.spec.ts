import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GOOSE_E2E_SKIP_REASON, gooseTest } from '../goose-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { bashToolCall, goosePermissionJudgmentToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { sendMessage } from '../helpers/ui'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest('shows a child tool request before the child finishes', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  void authenticatedGooseWorkspace
  await expectNoRegistryRows(page, leapmuxServer)
  const row = await withCleanup(async () => {
    await modelScript.rule(
      {
        name: 'the child runs its shell probe',
        when: { user: '^(?:Subagent ID: [^\\n]*\\n+)?Run `echo goose-live`' },
        respond: { toolCalls: [bashToolCall(AgentProvider.GOOSE, 'child-shell', 'echo goose-live')] },
        once: true,
      },
      {
        name: 'the child answers after its shell probe',
        when: { user: '^(?:Subagent ID: [^\\n]*\\n+)?Run `echo goose-live`' },
        respond: { text: 'GOOSE_CHILD_LIVE_DONE', gate: 'goose-child-final' },
      },
      {
        name: 'the permission judge clears the delegate and shell',
        when: { system: 'permission-safety classifier' },
        respond: { toolCalls: [goosePermissionJudgmentToolCall('judge-goose-live', ['spawn-goose-live', 'child-shell'])] },
      },
    )
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(AgentProvider.GOOSE, 'spawn-goose-live', {
        description: 'Run the live shell probe',
        prompt: modelScript.prompt('Run `echo goose-live` and report the result.'),
      })] },
      { text: 'GOOSE_ROOT_LIVE_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Delegate the live shell probe to a child.'))
    await modelScript.waitForGate('goose-child-final')
    const row = await requireRegistryRow(page)

    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'echo goose-live' }).first()).toBeVisible()
    await expect(row).not.toHaveAttribute('data-status', 'completed')

    return row
  }, async () => {
    await modelScript.releaseGateIfHeld('goose-child-final')
  })
  await modelScript.waitForSteps(2)
  await expectRowBecomesFinal(page, row)
})
