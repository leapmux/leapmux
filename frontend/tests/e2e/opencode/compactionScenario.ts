import type { NativeCompactionOptions } from '../helpers/manualCompaction'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { compactionNoticeRows, expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseFailedNativeCompaction, exerciseNativeCompaction, MANUAL_COMPACTION_SUMMARY, OLDER_CONTEXT_MARKER, requestNativeCompaction } from '../helpers/manualCompaction'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, expectSameNativeSession, nativeScenarioModelContextText } from '../helpers/nativeScenario'
import { waitForAgentIdle } from '../helpers/ui'
import { openCodeCompactionEvents } from './compactionNotice'

const summaryMarker = 'Create a new anchored summary from the conversation history'

/**
 * The native compaction of OpenCode, and of Kilo, which builds on OpenCode and sends the same summarizer prompt.
 * The summarizer request falls through to the fallback, which answers the summary.
 */
export const OPENCODE_COMPACTION: NativeCompactionOptions = {
  summary: { route: 'fallback', requestMarker: summaryMarker },
}

/** Prove that a successful native summary creates one stored daemon event and one divider. */
export async function exerciseOpenCodeCompactionNotice(context: ManagedNativeScenarioContext): Promise<void> {
  const before = await currentNativeAgent(context)
  await exerciseNativeCompaction(context, {
    ...OPENCODE_COMPACTION,
    summaryGate: 'opencode-family-compaction-notice',
    onHeld: async () => {
      await expect(compactionNoticeRows(context.page)).toHaveCount(0)
      expect(openCodeCompactionEvents(await readNativeMessageSnapshot(context, before.id))).toEqual([])
    },
  })
  expectSameNativeSession(before, await currentNativeAgent(context), 'Native compaction')
  const events = openCodeCompactionEvents(await readNativeMessageSnapshot(context, before.id))
  expect(events).toHaveLength(1)
  expect(events).toMatchObject([{ type: 'session.compacted', properties: { sessionID: before.agentSessionId } }])
  await expectCompactionNoticeAfterReload(context.page, { divider: true })
  expectSameNativeSession(before, await currentNativeAgent(context), 'Reloading the completed compaction')
  expect(openCodeCompactionEvents(await readNativeMessageSnapshot(context, before.id)))
    .toEqual(events)
}

/** A refused summary emits no completion; an explicit retry uses the preserved context. */
export async function exerciseFailedOpenCodeCompactionNotice(context: ManagedNativeScenarioContext): Promise<void> {
  const before = await currentNativeAgent(context)
  await exerciseFailedNativeCompaction(context, { summary: { route: 'rule', when: { body: summaryMarker } } })
  expect(openCodeCompactionEvents(await readNativeMessageSnapshot(context, before.id))).toEqual([])
  await expectCompactionNoticeAfterReload(context.page, { count: 0, divider: true })
  expect(openCodeCompactionEvents(await readNativeMessageSnapshot(context, before.id))).toEqual([])
  // OpenCode can finish a pending summary without answering the prompt that
  // triggered it. Retry the command before sending the continuation prompt.
  const recoveryRule = 'opencode-family-compaction-recovery'
  await context.modelScript.rule({
    name: recoveryRule,
    priority: 'high',
    once: true,
    when: { body: summaryMarker },
    respond: { text: MANUAL_COMPACTION_SUMMARY },
  })
  await requestNativeCompaction(context)
  await expect.poll(async () => (await context.modelScript.status()).ruleMatches[recoveryRule] ?? 0).toBe(1)
  await waitForAgentIdle(context.page)
  const status = await context.modelScript.status()
  expect(status.ruleMatches[recoveryRule]).toBe(1)
  const recoveryRequests = status.requests.filter(request => request.rule === recoveryRule)
  expect(recoveryRequests).toHaveLength(1)
  expect(JSON.stringify(recoveryRequests[0]?.body)).toContain(OLDER_CONTEXT_MARKER)
  const next = await sendNativeAnswer(context, 'Continue after the successful summary retry.', 'The original conversation remains available.')
  expect(nativeScenarioModelContextText(context, next)).toContain(MANUAL_COMPACTION_SUMMARY)
  expectSameNativeSession(before, await currentNativeAgent(context), 'Recovering from a refused summary')
  expect(openCodeCompactionEvents(await readNativeMessageSnapshot(context, before.id))).toHaveLength(1)
  await expectCompactionNoticeAfterReload(context.page, { divider: true })
}
