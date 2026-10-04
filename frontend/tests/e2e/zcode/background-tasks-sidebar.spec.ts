import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

zcodeTest('background-tasks-sidebar: routes the prompt, tools, and final report into the child tab', async ({
  authenticatedZCodeWorkspace,
  page,
  modelScript,
  leapmuxServer,
}) => {
  void authenticatedZCodeWorkspace
  await expectNoRegistryRows(page, leapmuxServer)

  // The child runs a tool of its own before it answers, which is what puts a
  // tool row in the CHILD transcript for the routing assertions below. Two
  // rules rather than one, because the child's two turns differ: the first
  // sees its prompt, the second sees the shell result.
  const row = await withCleanup(async () => {
    await modelScript.rule(
      {
        name: 'the child runs its shell probe',
        when: { user: 'printf zcode-tool-ok' },
        respond: { toolCalls: [bashToolCall(AgentProvider.ZCODE, 'child-shell', 'printf zcode-tool-ok')] },
        once: true,
      },
      {
        name: 'the child answers after its shell probe',
        // The same matcher as the rule above, which `once` has already spent.
        // It matches on `user` rather than on `body` for the reason that costs a
        // run to find: the ROOT's second turn carries the spawn tool call, whose
        // arguments quote the child's prompt, so a body matcher answers the root
        // with the CHILD's line and the queued root answer is never consumed.
        when: { user: 'printf zcode-tool-ok' },
        respond: { text: 'ZCODE_CHILD_PONG', gate: 'zcode-child-final' },
      },
    )
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.ZCODE, 'spawn-zcode', {
        description: 'Run the shell probe',
        prompt: modelScript.prompt('Use Bash to run printf zcode-tool-ok, then reply with exactly ZCODE_CHILD_PONG.'),
      })],
    })
    await modelScript.queue({ text: 'ZCODE_ROOT_DONE' })
    await sendMessage(page, modelScript.prompt('Spawn one subagent to run the shell probe, then report what it said.'))
    await modelScript.waitForGate('zcode-child-final')
    const row = await requireRegistryRow(page)

    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'printf zcode-tool-ok' }).first()).toBeVisible()
    await expect(row).not.toHaveAttribute('data-status', 'completed')

    return row
  }, async () => {
    await modelScript.releaseGateIfHeld('zcode-child-final')
  })
  await modelScript.waitForSteps(2)
  await waitForAgentIdle(page, 180_000)
  await expectRowBecomesFinal(page, row)

  await expect(userBubbles(page).filter({ hasText: 'ZCODE_CHILD_PONG' })).toBeVisible()
  await expect(page.locator('[data-testid="message-bubble"]:visible')
    .filter({ hasText: 'Subagent reported' })
    .filter({ hasText: /ZCODE_CHILD_PONG/ })).toBeVisible()
})
