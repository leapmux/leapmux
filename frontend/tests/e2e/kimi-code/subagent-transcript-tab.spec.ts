import { expect } from '@playwright/test'
import { bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, toolRows, userBubbles } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'
import { kimiChildTurn, prepareKimiChildRun } from './childScenario'

kimiTest.describe('runs Kimi Code subagents and background tasks', () => {
  kimiTest.beforeEach(async ({ native }) => {
    await prepareKimiChildRun(native.page)
  })

  kimiTest('routes the prompt, tools, and final report into the child tab', async ({ native }) => {
    const { page, modelScript } = native
    await expectNoRegistryRows(page, native.leapmuxServer)

    await modelScript.rule(
      {
        name: 'the child runs its shell probe',
        when: kimiChildTurn('printf kimi-child-tool-ok'),
        respond: { toolCalls: [bashToolCall(native.provider, 'child-shell', 'printf kimi-child-tool-ok')] },
        once: true,
      },
      {
        name: 'the child answers after its shell probe',
        when: kimiChildTurn('printf kimi-child-tool-ok'),
        respond: { text: 'KIMI_CHILD_PONG' },
      },
    )
    const start = await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(native.provider, 'spawn-kimi', {
          description: 'Run the shell probe',
          prompt: modelScript.prompt('Use Bash to run printf kimi-child-tool-ok, then reply with exactly KIMI_CHILD_PONG.'),
        })],
      },
      { text: 'KIMI_ROOT_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one subagent to run the shell probe, then report what it said.'))
    await modelScript.waitForSteps(start + 2)
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_ROOT_DONE' })).not.toHaveCount(0)

    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await expect(row).toContainText('Run the shell probe')
    await openChildTabFromRow(page, row)

    await expect(userBubbles(page).filter({ hasText: 'printf kimi-child-tool-ok' })).not.toHaveCount(0)
    await expect(toolRows(page).filter({ hasText: 'kimi-child-tool-ok' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_CHILD_PONG' })).not.toHaveCount(0)
  })
})
