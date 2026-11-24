import { expect } from '@playwright/test'
import { finishCleanup, withCleanup } from '../helpers/cleanup'
import { exerciseQueuedTurnWithoutSteering } from '../helpers/nativeToolSteering'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { QUEUED_INPUT_ROW_TESTID, queuedInputRow } from '../helpers/steer'
import { expectRowBecomesFinal, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, inputQueue, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'
import { registerLettaChildNoticeRule } from './childNoticeRule'

lettaTest('refuses mid-turn steering and runs the queued prompt in a separate native turn', async ({ native }) => {
  await exerciseQueuedTurnWithoutSteering(native)
})

lettaTest('holds queued input while a background subagent runs and dispatches immediately on Preempt', async ({ native }) => {
  const { page, modelScript } = native
  const childGate = 'letta-bg-queue-preempt-gate'
  const childPrompt = modelScript.prompt('Hold at LETTA_BG_PREEMPT_CHILD_MARKER until released.')
  await withCleanup(async () => {
    await modelScript.rule(
      {
        name: 'letta background subagent holds on gate',
        when: {
          user: 'LETTA_BG_PREEMPT_CHILD_MARKER',
          lastMessage: { role: 'user' },
        },
        respond: { gate: childGate, text: 'LETTA_BG_PREEMPT_CHILD_DONE' },
        once: true,
      },
      {
        name: 'letta answers preempted message while background subagent runs',
        when: {
          user: 'preempt behind background subagent',
          lastMessage: { role: 'user' },
        },
        respond: { text: 'LETTA_PREEMPTED_WHILE_BG_RUNNING_REPLY' },
        once: true,
      },
    )
    const start = await modelScript.queue(
      {
        toolCalls: [
          spawnSubagentToolCall(native.provider, 'letta-bg-preempt-child', {
            description: 'Background preempt worker',
            prompt: childPrompt,
          }),
        ],
      },
      { text: 'LETTA_BG_PREEMPT_ROOT_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Spawn a background subagent for Preempt queue hold test.'))
    await modelScript.waitForSteps(start + 2)
    await modelScript.waitForGate(childGate)
    await expect(assistantBubbles(page).filter({ hasText: 'LETTA_BG_PREEMPT_ROOT_DONE' })).toHaveCount(1)
    const bgRow = await requireRegistryRow(page)
    await expect(bgRow).toHaveAttribute('data-status', 'running')

    await sendMessage(page, modelScript.prompt('preempt behind background subagent'))
    const queued = queuedInputRow(page, 'preempt behind background subagent')
    await expect(inputQueue(page)).toContainText('preempt behind background subagent')
    await expect(queued).toBeVisible()
    await expect(userBubbles(page).filter({ hasText: 'preempt behind background subagent' })).toHaveCount(0)
    const preemptButton = queued.getByRole('button', { name: 'Preempt', exact: true })
    await expect(preemptButton).toBeVisible()

    await preemptButton.click()
    await expect(userBubbles(page).filter({ hasText: 'preempt behind background subagent' })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'LETTA_PREEMPTED_WHILE_BG_RUNNING_REPLY' })).toHaveCount(1)
    await expect(page.getByTestId(QUEUED_INPUT_ROW_TESTID)).toHaveCount(0)
    await expect(bgRow).toHaveAttribute('data-status', 'running')

    await registerLettaChildNoticeRule(native, {
      name: 'letta root handles preempt test child completion notice',
      spawnCallId: 'letta-bg-preempt-child',
      description: 'Background preempt worker',
      report: 'LETTA_BG_PREEMPT_CHILD_DONE',
      reply: 'LETTA_BG_PREEMPT_NOTICE_DONE',
      once: true,
    })
    await modelScript.releaseGate(childGate)
    await expectRowBecomesFinal(page, bgRow)
    await expect(assistantBubbles(page).filter({ hasText: 'LETTA_BG_PREEMPT_NOTICE_DONE' })).toHaveCount(1)
    await waitForAgentIdle(page)
  }, () => finishCleanup([
    modelScript.releaseGateIfHeld(childGate),
  ]))
})
