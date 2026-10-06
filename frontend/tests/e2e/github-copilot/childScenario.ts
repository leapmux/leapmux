import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeChildProfile } from '../helpers/runningChildProof'
import { expect } from '@playwright/test'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { applyPermissionPreset, assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { copilotChildTaskMatcher, readCopilotChildTaskId } from './childIdentity'

/**
 * The facts of a held Copilot child.
 * The Bypass preset lets the spawn run without a permission request. The start event of the child selects it by the
 * spawn call, so the row title does not.
 */
export const COPILOT_CHILD: NativeChildProfile = {
  childTask: copilotChildTaskMatcher,
  rowTitleHoldsDescription: false,
  prepare: context => applyPermissionPreset(context.page, 'bypass'),
  resolveTaskId: (context, parentId, child) => readCopilotChildTaskId(context, parentId, child.spawn.id),
}

/**
 * Spawn one child that answers one word, and follow its prompt, its own answer, and its completion into its own tab.
 * The transcript tab cell and the background task cell both run this scenario.
 */
export async function exerciseCopilotChildTranscript(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  await expectNoRegistryRows(page, context.leapmuxServer)

  // The child's prompt carries the marker, so its own turn reaches this
  // script. The rule matches text that the parent prompt does not carry, or
  // the parent's own turn would take this answer instead of the queued spawn.
  await modelScript.rule({
    name: 'the child answers with its exact word',
    // Not anchored. Copilot puts a `<current_datetime>` block before each user
    // turn, so `^` never matches the task. No anchor is necessary: Copilot
    // returns a tool result as a `tool` message, so the last user text of a
    // parent turn stays the original prompt and never holds the child prompt.
    when: { user: 'Reply with exactly COPILOT_CHILD_PONG' },
    respond: { text: 'COPILOT_CHILD_PONG' },
  })
  const start = await modelScript.queue(
    {
      toolCalls: [spawnSubagentToolCall(context.provider, 'spawn-copilot', {
        description: 'Ask the subagent for one word',
        prompt: modelScript.prompt('Reply with exactly COPILOT_CHILD_PONG.'),
      })],
    },
    { text: 'COPILOT_ROOT_DONE' },
  )
  await sendMessage(page, modelScript.prompt('Spawn one subagent, wait for it, then report what it said.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)

  const row = await requireRegistryRow(page)
  await expectRowBecomesFinal(page, row)
  await openChildTabFromRow(page, row)
  await expect(userBubbles(page).filter({ hasText: 'COPILOT_CHILD_PONG' })).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: /^COPILOT_CHILD_PONG$/ })).toBeVisible()
}
