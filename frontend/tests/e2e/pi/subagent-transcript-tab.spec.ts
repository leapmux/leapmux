import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { sendMessage, userBubbles } from '../helpers/ui'
import { PI_E2E_SKIP_REASON, piTest } from '../pi-fixtures'
import { registerPiChildNoticeRule } from './childNoticeRule'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest('foreground subagent shows a live activity row', async ({
  authenticatedPiWorkspace,
  page,
  modelScript,
  leapmuxServer,
}) => {
  void authenticatedPiWorkspace
  const gate = `pi-counting-child-${crypto.randomUUID()}`
  await withCleanup(async () => {
    await expectNoRegistryRows(page, leapmuxServer)

    // The child prompt carries the scenario marker. Its anchored rule cannot
    // answer a parent request that contains the prompt in a tool argument.
    await modelScript.rule({
      name: 'the child works through its multi-step task',
      // Match the start of the actual child prompt.
      when: { user: '^List three fruits' },
      respond: { gate, text: 'Apple, banana, cherry. One, two, three, four, five. Done.' },
    })
    await modelScript.queue({
      toolCalls: [spawnSubagentToolCall(AgentProvider.PI, 'spawn-pi', {
        description: 'Run the fruit task',
        prompt: modelScript.prompt('List three fruits, then count to five, then report done.'),
      })],
    })
    await modelScript.queue({ text: 'The subagent listed three fruits and counted to five.' })
    await sendMessage(page, modelScript.prompt('Spawn one subagent for the counting task and report what it says.'))
    await modelScript.waitForGate(gate)
    await registerPiChildNoticeRule(modelScript, { name: 'the parent acknowledges the subagent notification', spawnCallId: 'spawn-pi', description: 'Run the fruit task', report: 'Apple, banana, cherry. One, two, three, four, five. Done.', reply: 'The subagent finished the counting task.' })
    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(2)

    // Require the actual native row. Idle waits also track active child tasks.
    const row = await requireRegistryRow(page)

    await expectRowBecomesFinal(page, row)
    await expectSectionPersists(page)
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: /list three fruits/i })).toBeVisible()
    // The report bubble contains its label and the native child answer.
    // An exact label match would reject that complete report.
    await expect(page.locator('[data-testid="message-bubble"]:visible')
      .filter({ hasText: 'Subagent reported' })
      .filter({ hasText: /Apple, banana, cherry/ })).toBeVisible()
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })
})
