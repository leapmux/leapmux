import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeChildProfile } from '../helpers/runningChildProof'
import { expect } from '@playwright/test'
import { nativeAgentById } from '../helpers/nativeScenario'
import { goosePermissionJudgmentToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { sendMessage, subagentReportBubble } from '../helpers/ui'
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
}

/**
 * Spawn one delegate that runs a shell probe, open its read-only transcript from its row, and require its report, its
 * final row, and its Worker record. The transcript tab, background task, and send cells all run this scenario.
 */
export async function exerciseGooseDelegateTranscript(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  await expectNoRegistryRows(page, context.leapmuxServer)
  // The child's prompt carries the marker, so the turns it runs on its own reach this script.
  await modelScript.rule({
    name: 'the child reports the shell result',
    when: gooseChildTaskMatcher('Run `echo goose-done`'),
    respond: { text: 'The command printed goose-done.' },
  })
  // Goose runs a permission-safety classifier turn of its own, with its own
  // system prompt, before it lets a tool run. It reads the answer from a
  // `platform__tool_by_tool_permission` tool call and takes any other answer
  // as "not read-only". That holds the tool for an approval that never comes,
  // so the delegate never spawns and no child turn reaches the script. The
  // test states the call ID of the spawn, so the judge can clear it.
  await modelScript.rule({
    name: 'the permission judge clears the delegate',
    when: { system: 'permission-safety classifier' },
    respond: {
      toolCalls: [goosePermissionJudgmentToolCall('judge-goose', ['spawn-goose'])],
    },
  })
  const start = await modelScript.queue(
    {
      toolCalls: [spawnSubagentToolCall(context.provider, 'spawn-goose', {
        description: 'Run the shell probe',
        prompt: modelScript.prompt('Run `echo goose-done` and tell me the result.'),
      })],
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
