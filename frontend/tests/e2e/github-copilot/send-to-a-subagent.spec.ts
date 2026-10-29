import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { printfMarkerCommand } from '../helpers/shellArguments'
import { steerQueuedInput } from '../helpers/steer'
import { openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, tabById, userBubbles } from '../helpers/ui'
import { copilotChildTaskMatcher } from './childIdentity'
import { bypassToolRequests } from './scenarios'

// Copilot passes a message only to a subagent that runs in the background: a foreground subagent never reads it. A
// background subagent stays idle after each turn, and the Worker keeps its row paused and its tab open. The Worker sends
// a message from the subagent's tab with session.tasks.sendMessage. Copilot queues a message to a running subagent for
// the turn after the running one, and starts a turn of an idle subagent for it.
copilotTest('sends messages into a background subagent while it runs and while it waits', async ({ native }) => {
  const { page, modelScript } = native
  await bypassToolRequests(native)
  const gate = 'copilot-child-send'
  const childTask = 'Run the child marker command, then wait for the reader.'
  const steeredRule = 'the child reads the message sent while it ran'
  const idleRule = 'the child reads the message sent while it waited'
  await modelScript.rule(
    {
      name: 'the child runs its first tool',
      when: copilotChildTaskMatcher(childTask),
      respond: {
        gate,
        toolCalls: [
          bashToolCall(native.provider, 'child-shell', printfMarkerCommand('CHILD_READY_', 42)),
        ],
      },
      once: true,
    },
    {
      name: 'the child answers its tool',
      when: {
        ...copilotChildTaskMatcher(childTask),
        lastMessage: { role: 'tool' },
      },
      respond: { text: 'The child marker command ran.' },
      once: true,
    },
    {
      name: steeredRule,
      when: { user: 'Also say CHILD_STEERED' },
      respond: { text: 'CHILD_STEERED' },
      once: true,
    },
    {
      name: idleRule,
      when: { user: 'Now say CHILD_AGAIN' },
      respond: { text: 'CHILD_AGAIN' },
      once: true,
    },
  )
  // Copilot tells the parent each time the background subagent goes idle, and the parent answers the notice.
  await modelScript.fallback({ text: 'The background subagent is idle.' })
  const { row, childId, parentId } = await withCleanup(async () => {
    await modelScript.queue(
      {
        toolCalls: [
          spawnSubagentToolCall(native.provider, 'spawn-child-send', {
            description: 'Answer the messages from the child tab',
            prompt: modelScript.prompt(childTask),
            background: true,
          }),
        ],
      },
      { text: 'The child runs in the background.' },
    )
    const parent = await selectedAgentTabId(page)
    await sendMessage(page, modelScript.prompt('Spawn one background subagent for the messages from its tab.'))
    await modelScript.waitForGate(gate)
    const spawned = await requireRegistryRow(page)
    await expect(spawned).toHaveAttribute('data-status', 'running')
    const child = await openChildTabFromRow(page, spawned)
    await steerQueuedInput(page, { message: 'Also say CHILD_STEERED.', match: 'Also say' })
    await modelScript.releaseGate(gate)
    return { row: spawned, childId: child, parentId: parent }
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })

  await expect.poll(async () => (await modelScript.status()).ruleMatches[steeredRule] ?? 0).toBe(1)
  await expect(assistantBubbles(page).filter({ hasText: 'CHILD_STEERED' }).first()).toBeVisible()
  await expect(userBubbles(page).filter({ hasText: /Also say CHILD.*STEERED/ }).first()).toBeVisible()
  // The subagent waits for the next message, so its row stays open.
  await expect(row).toHaveAttribute('data-status', 'paused')

  await sendMessage(page, 'Now say CHILD_AGAIN.')
  await expect.poll(async () => (await modelScript.status()).ruleMatches[idleRule] ?? 0).toBe(1)
  await expect(assistantBubbles(page).filter({ hasText: 'CHILD_AGAIN' }).first()).toBeVisible()
  await expect(row).toHaveAttribute('data-status', 'paused')

  // The parent's transcript keeps none of the subagent's answers.
  await tabById(page, parentId).click()
  await expect(assistantBubbles(page).filter({ hasText: /^CHILD_(?:STEERED|AGAIN)$/ })).toHaveCount(0)
  await tabById(page, childId).click()
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/505.
