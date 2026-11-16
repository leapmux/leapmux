import type { MockModelRule } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeChildProfile } from '../helpers/runningChildProof'
import { expect } from '@playwright/test'
import { escapeRegExp } from '../../../src/lib/regexp'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { childTaskAfter } from '../helpers/runningChildProof'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'

/**
 * The facts of a held OpenCode child.
 * The native prompt starts with a bootstrap line before its task. The matcher keeps that prefix separate from the
 * parent prompt. `opencode/workflow-grouping.spec.ts` selects each child by the title of its row.
 */
export const OPENCODE_CHILD: NativeChildProfile = {
  childTask: task => childTaskAfter('You are a subagent spawned by another session\\.\\n', task),
  rowTitleHoldsDescription: true,
  beforeRelease: async (context, child) => { await context.modelScript.rule(openCodeChildReportRule(child.description)) },
}

/** Native child completion starts another parent turn with this exact report envelope. */
export function openCodeChildReportRule(description: string): MockModelRule {
  return {
    name: `the OpenCode parent receives ${description}`,
    when: { user: `^<subagent sessionID="[^\"]+" state="[^\"]+" description="${escapeRegExp(description)}">\\n[\\s\\S]*\\n</subagent>$` },
    respond: { text: 'The parent received the child report.' },
  }
}

/**
 * Spawn one child that answers one word, then follow its row from running to final, and its prompt and report into
 * its own tab. The transcript tab cell and the background task cell both run this scenario.
 */
export async function exerciseOpencodeSpawnTranscript(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  await expectNoRegistryRows(page, context.leapmuxServer)
  await modelScript.rule(openCodeChildReportRule('Ask the subagent for one word'))
  // The child's prompt carries the marker, so the turns it runs on its own
  // reach this script rather than the ambient scenario. The rule matches text
  // that the parent prompt does not carry, or the parent's own turn would take
  // this answer instead of the queued spawn.
  await modelScript.rule({
    name: 'the child answers its one-word task',
    // Anchored. A parent turn that carries the tool request holds this whole
    // prompt, so an unanchored pattern answers the parent's turn, and the
    // parent never consumes its queued step.
    when: OPENCODE_CHILD.childTask('Reply with the single word PONG.'),
    respond: { text: 'PONG' },
  })
  const start = await modelScript.queue(
    {
      toolCalls: [
        spawnSubagentToolCall(context.provider, 'spawn-opencode', {
          description: 'Ask the subagent for one word',
          prompt: modelScript.prompt('Reply with the single word PONG.'),
        }),
      ],
    },
    { text: 'The subagent reported PONG.' },
  )
  await sendMessage(page, modelScript.prompt('Spawn one subagent and report what it says.'))
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)

  const row = await requireRegistryRow(page)
  await expectRowBecomesFinal(page, row)
  await expectSectionPersists(page)
  await openChildTabFromRow(page, row)
  await expect(userBubbles(page).filter({ hasText: 'Reply with the single word PONG' })).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: /^PONG$/ })).toBeVisible()
}
