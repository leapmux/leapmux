import type { Page } from '@playwright/test'
import type { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelMatcher, MockModelRule, MockModelStep } from './mockModelScript'
import type { ModelScript } from './modelScriptFixture'
import { writeFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { finishCleanup, withCleanup } from './cleanup'
import { expandNativeResultView } from './nativeResultView'
import { selectedAgentTabId } from './nativeScenario'
import { readToolCall, spawnSubagentToolCall } from './providerToolCalls'
import { uniqueMarker } from './shellArguments'
import { openChildTabFromRow, requireRegistryRow } from './subagentRegistry'
import { assistantBubbles, messageContents, sendMessage, tabById, toolCallRow, toolRows, userBubbles } from './ui'

/** The final child answer when the spec supplies no native final response. */
const DEFAULT_CHILD_ANSWER = 'CHILD_LIVE_DONE'

/** The call ID of the native Read that the helper scripts for the child. */
const LIVE_CHILD_READ_CALL_ID = 'live-child-read'

export interface LiveChildSpec {
  provider: AgentProvider
  childWhen: MockModelMatcher
  childTask: string
  parentTask: string
  /**
   * The native final response of the child. The default is the text CHILD_LIVE_DONE.
   * The gate holds this response, with or without the earlier Read of toolProof.
   */
  childResponse?: Omit<MockModelStep, 'gate'>
  holdParentAnswer?: boolean
  background?: boolean
  allowPaused?: boolean
  /**
   * Make the child read a file with a computed marker before its final response.
   *
   * `expandResult` expands the result view of that Read before the helper looks for the marker.
   * A result view shows only its first `COLLAPSED_RESULT_ROWS` rows (`~/components/chat/results/collapse`)
   * until the reader expands it. Set `expandResult` when the native Read result puts header rows
   * before the file text, so the marker row is not in the page while the view is collapsed.
   * Leave it unset when the marker is in the first rows.
   */
  toolProof?: { workingDir: string, expandResult?: boolean }
  /** Register provider-owned report handling while the real child remains held. */
  beforeRelease?: () => Promise<void>
  /** Complete provider-owned work that starts after the native child finishes. */
  afterComplete?: () => Promise<void>
}

/** The file that the child reads, and the computed marker that only that file holds. */
interface MarkerRead {
  filePath: string
  marker: string
}

/**
 * Script the child turns: an optional native Read of the marker file, then the native final response.
 * The gate holds only the final response.
 * Thus the Read result reaches the child tab while the child still runs.
 */
function liveChildRules(spec: LiveChildSpec, gate: string, read: MarkerRead | undefined): MockModelRule[] {
  const finalResponse: MockModelStep = { ...(spec.childResponse ?? { text: DEFAULT_CHILD_ANSWER }), gate }
  if (!read)
    return [{ name: 'the held child answer', when: spec.childWhen, respond: finalResponse, once: true }]
  return [
    {
      name: 'the child reads its marker file',
      when: spec.childWhen,
      respond: { toolCalls: [readToolCall(spec.provider, LIVE_CHILD_READ_CALL_ID, read.filePath)] },
      once: true,
    },
    // Only the Read result supplies the marker, so this rule answers the request that follows the Read.
    { name: 'the held child answer after the read', when: { body: read.marker }, respond: finalResponse, once: true },
  ]
}

/** Hold a child model answer while its prompt or tool result is visible in a running child tab. */
export async function exerciseLiveChildTranscript(page: Page, modelScript: ModelScript, spec: LiveChildSpec): Promise<void> {
  const gate = `live-child-${spec.provider}`
  const parentGate = spec.holdParentAnswer ? `live-parent-${spec.provider}` : undefined
  const read: MarkerRead | undefined = spec.toolProof
    ? { filePath: join(spec.toolProof.workingDir, `live-child-${spec.provider}.txt`), marker: uniqueMarker('CHILDREAD') }
    : undefined
  // The parent is the agent on screen before the child spawns.
  const parentTabID = read ? await selectedAgentTabId(page) : ''
  if (read)
    writeFileSync(read.filePath, `${read.marker}\n`)
  await withCleanup(async () => {
    await modelScript.rule(...liveChildRules(spec, gate, read))
    await modelScript.queue(
      { toolCalls: [spawnSubagentToolCall(spec.provider, 'spawn-live-child', {
        description: 'Answer the live child task',
        prompt: modelScript.prompt(spec.childTask),
        ...(spec.background === undefined ? {} : { background: spec.background }),
      })] },
      { text: 'The child task ended.', ...(parentGate ? { gate: parentGate } : {}) },
    )
    await sendMessage(page, modelScript.prompt(spec.parentTask))
    await modelScript.waitForGate(gate)
    await spec.beforeRelease?.()
    const row = await requireRegistryRow(page)
    await expect(row).toHaveAttribute('data-status', spec.allowPaused ? /^(?:running|paused)$/ : 'running')
    await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')
    const childTabID = await openChildTabFromRow(page, row)
    await expect(userBubbles(page).filter({ hasText: spec.childTask }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: DEFAULT_CHILD_ANSWER })).toHaveCount(0)
    if (read) {
      await expect(toolRows(page).filter({ hasText: basename(read.filePath) }).first()).toBeVisible()
      if (spec.toolProof?.expandResult)
        await expandNativeResultView(toolCallRow(page, LIVE_CHILD_READ_CALL_ID))
      await expect(messageContents(page).filter({ hasText: read.marker }).first()).toBeVisible()
      await tabById(page, parentTabID).click()
      await expect(messageContents(page).filter({ hasText: read.marker })).toHaveCount(0)
      await tabById(page, childTabID).click()
    }
    await modelScript.releaseGate(gate)
    if (parentGate) {
      await modelScript.waitForGate(parentGate)
      await modelScript.releaseGate(parentGate)
    }
  }, () => finishCleanup([
    modelScript.releaseGateIfHeld(gate),
    ...(parentGate ? [modelScript.releaseGateIfHeld(parentGate)] : []),
  ]))
  await completeLiveChildTranscript(modelScript, spec.afterComplete)
}

/** Finish the initial model steps before provider-owned completion work. */
export async function completeLiveChildTranscript(modelScript: Pick<ModelScript, 'waitForSteps'>, afterComplete?: () => Promise<void>): Promise<void> {
  await modelScript.waitForSteps()
  await afterComplete?.()
}
