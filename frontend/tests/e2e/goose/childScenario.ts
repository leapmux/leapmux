import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeChildProfile } from '../helpers/runningChildProof'
import { expect } from '@playwright/test'
import { nativeAgentById, selectedAgentTabId } from '../helpers/nativeScenario'
import { gooseFinalOutputToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { sendMessage, subagentReportBubble, tabById } from '../helpers/ui'
import { expectReadOnlySubagentReason } from '../helpers/unsupportedSubagent'
import { gooseChildTaskMatcher, readGooseChildTaskId } from './childIdentity'
import { bypassToolRequests } from './scenarios'

/**
 * The facts of a held Goose child.
 * The Bypass preset lets the delegate run without the permission judge. The task ID of the delegate frame selects the
 * child, so the row title does not.
 */
export const GOOSE_CHILD: NativeChildProfile = {
  childTask: gooseChildTaskMatcher,
  rowTitleHoldsDescription: false,
  prepare: bypassToolRequests,
  resolveTaskId: (context, parentId, child) => readGooseChildTaskId(context, parentId, child.spawn.id, child.prompt),
  // The child ends through its built-in final-output tool. A plain-text answer
  // leaves its runtime requesting another turn, which no script serves.
  finalStep: child => ({
    toolCalls: [
      gooseFinalOutputToolCall(`final-${child.spawn.id}`, child.report),
    ],
  }),
}

/**
 * Spawn one delegate that runs a shell probe, open its read-only transcript from its row, and require its report, its
 * final row, and its Worker record. The transcript tab, background task, and send cells all run this scenario.
 */
export async function exerciseGooseDelegateTranscript(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  const parentId = await selectedAgentTabId(page)
  await expectNoRegistryRows(page, context.leapmuxServer)
  // The Bypass preset sets the Auto mode: the delegate and the child's
  // built-in final-output tool both run without an approval. In the default
  // Smart Approve mode the child waits for a permission card that no reader
  // answers, and the parent's turn never continues.
  await bypassToolRequests(context)
  // The child's prompt carries the marker, so the turns it runs on its own reach this script.
  // The child ends by calling its built-in final-output tool: a plain-text
  // answer leaves its runtime requesting another turn, which no script serves.
  await modelScript.rule({
    name: 'the child reports the shell result',
    when: gooseChildTaskMatcher('Run `echo goose-done`'),
    respond: {
      toolCalls: [
        gooseFinalOutputToolCall('child-final-goose', 'The command printed goose-done.'),
      ],
    },
  })
  const start = await modelScript.queue(
    {
      toolCalls: [
        spawnSubagentToolCall(context.provider, 'spawn-goose', {
          description: 'Run the shell probe',
          prompt: modelScript.prompt('Run `echo goose-done` and tell me the result.'),
        }),
      ],
    },
    { text: 'The subagent reported goose-done.' },
  )
  await sendMessage(page, modelScript.prompt('Delegate the shell probe to a subagent and report the result.'))
  await modelScript.waitForSteps(start + 2)

  // The spawn itself creates the transcript. A child that uses no tool still
  // keeps its prompt and report.
  const row = await requireRegistryRow(page)
  const childId = await openChildTabFromRow(page, row)
  // Goose cannot steer a subagent, so the child tab is a read-only transcript, and its composer states why.
  await expectReadOnlySubagentReason(page)
  await expect(subagentReportBubble(page, /goose-done/)).toBeVisible()
  await expectRowBecomesFinal(page, row)
  await tabById(page, parentId).click()
  await expectSectionPersists(page)

  // Worker-backed: the child agent exists, with its parent linkage. The read
  // asks the Worker about the child ID of the registry row, because the
  // tab projection of the Hub is empty here.
  await retryUntilPass(async () => {
    const child = await nativeAgentById(context, childId)
    expect(child && {
      hasParent: child.parentAgentId !== '',
      hasSpawnSpan: child.spawnSpanId !== '',
      acceptsMessages: child.acceptsMessages,
    }, `the Worker holds the child agent ${childId} with its parent links`).toEqual({
      hasParent: true,
      hasSpawnSpan: true,
      // Goose cannot steer a subagent, so the child tab is a read-only
      // transcript -- the same fact the read-only reason above shows.
      acceptsMessages: false,
    })
  })
}
