import type { AgentInfo } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import type { MockModelStep } from './mockModelScript'
import type { ManagedNativeScenarioContext } from './nativeScenario'
import type { ToolOutputControl } from './toolOutputControl'
import { expect } from '@playwright/test'
import { finishCleanup, withCleanup } from './cleanup'
import { currentNativeAgent, nativeTextStep } from './nativeScenario'
import { bashToolCall } from './providerToolCalls'
import { uniqueMarker } from './shellArguments'
import { createToolOutputControl } from './toolOutputControl'
import { observeSettledReceipts, waitForIdleSoundReceipt } from './turnEndSound'
import { answerControl, assistantBubbles, messageContents, sendMessage, waitForAgentIdle, waitForControlBanner } from './ui'

/** The state of the controlled output command when one of its segments holds. */
export interface OutputBoundary {
  phase: 'first' | 'second'
  firstMarker: string
  secondMarker: string
  firstLiveTail: string
  secondLiveTail: string
  count: number
}

/** The options that both generation progress scenarios take. */
interface NativeProgressCase {
  /** True when the provider shows a live counter that advances. False when no counter shows for the whole turn. */
  supported: boolean
  prepare?: () => Promise<void>
}

/** A streamed answer that holds after its first and its second chunk. */
export interface NativeTokenProgressCase extends NativeProgressCase {
  /** The step that the answer text and the stream join, such as a step that thinks before it answers. */
  step?: MockModelStep
}

/** The tool call ID of the controlled output command of `exerciseOutputByteProgress`. */
export const PROGRESS_OUTPUT_CALL_ID = 'native-progress-output'

/** A controlled shell command that holds after its first and its second output segment. */
export interface NativeOutputByteProgressCase extends NativeProgressCase {
  /** Allow the visible permission request of the command. */
  approveTool?: boolean
  outputMarkers?: { first: string, second: string }
  /**
   * Resolve when the native client started the controlled command and attached to its output.
   *
   * When set, the command holds its first output segment until this resolves. A native
   * client that streams a command's output live can drop what the command writes before
   * it attached (Codex does), so the first segment of a command that writes at once can
   * leave no live byte count. Read the start from the native client's own record.
   */
  waitForToolStart?: () => Promise<void>
  /**
   * Inspect the page while one output segment holds. The live tails are the text
   * that a live view of each segment shows (see `ToolOutputControl.firstLiveTail`).
   */
  afterOutputBoundary?: (boundary: OutputBoundary) => Promise<void>
  prepareCompletedResultView?: (callId: string) => Promise<void>
}

/** The text of the controlled output command that every boundary states. */
function outputBoundary(output: ToolOutputControl): Omit<OutputBoundary, 'phase' | 'count'> {
  return {
    firstMarker: output.firstMarker,
    secondMarker: output.secondMarker,
    firstLiveTail: output.firstLiveTail,
    secondLiveTail: output.secondLiveTail,
  }
}

/** Prepare the completed result before the two exact output-marker assertions. */
export async function verifyCompletedGenerationOutput(
  callId: string,
  markers: { first: string, second: string },
  operations: { prepareView?: (callId: string) => Promise<void>, assertVisible: (marker: string) => Promise<void> },
): Promise<void> {
  if (!callId || !markers.first || !markers.second || markers.first === markers.second)
    throw new Error('The completed generation proof requires a call ID and distinct output markers.')
  await operations.prepareView?.(callId)
  await operations.assertVisible(markers.first)
  await operations.assertVisible(markers.second)
}

export interface GenerationCounters {
  tokens?: number
  bytes?: number
}

interface GenerationProbeState {
  samples: string[]
  stop: () => void
}

declare global {
  interface Window {
    __nativeGenerationProbe?: GenerationProbeState
  }
}

/** Read only counter labels in the generation indicator. */
export function parseGenerationCounters(text: string): GenerationCounters {
  const counters: GenerationCounters = {}
  const tokens = text.match(/(?<![\d.])(\d+(?:\.\d+)?)(?:\s*([km]))?\s+tokens?\b/i)
  if (tokens?.[1]) {
    const scale = tokens[2]?.toLowerCase() === 'm' ? 1_000_000 : tokens[2]?.toLowerCase() === 'k' ? 1000 : 1
    counters.tokens = Number(tokens[1]) * scale
    if (!Number.isFinite(counters.tokens))
      throw new Error('The generation token counter is invalid.')
  }
  const bytes = text.match(/(\d+(?:\.\d+)?)\s*(B|KB|MB)\b/i)
  if (bytes?.[1]) {
    const unit = bytes[2]?.toUpperCase()
    const scale = unit === 'MB' ? 1024 ** 2 : unit === 'KB' ? 1024 : 1
    counters.bytes = Number(bytes[1]) * scale
    if (!Number.isFinite(counters.bytes))
      throw new Error('The generation byte counter is invalid.')
  }
  return counters
}

/**
 * Observe displayed counters before a native turn starts. The browser owns this state.
 *
 * An indicator counts as shown by the rule of `installThinkingIndicatorWatch` (`./thinkingIndicatorWatch.ts`): its
 * inline style states a `display` other than `none` and `grid-template-rows: 1fr`. A collapsed indicator stays in the
 * DOM with `0fr`, so its counters are not shown. The probe also skips an indicator that the page renders nowhere, such
 * as a hidden premeasure copy. The page runs this function, so the rule is written here again: the body cannot use a
 * name from outside itself.
 */
export function installGenerationObservation(): void {
  window.__nativeGenerationProbe?.stop()
  const samples: string[] = []
  const inspect = () => {
    for (const element of document.querySelectorAll<HTMLElement>('[data-testid="thinking-indicator"]')) {
      if (element.style.display === 'none' || element.style.gridTemplateRows !== '1fr')
        continue
      const style = getComputedStyle(element)
      if (element.getClientRects().length === 0 || style.display === 'none' || style.visibility === 'hidden')
        continue
      const text = [...element.querySelectorAll<HTMLElement>('[data-animated-count]')]
        .map(count => count.firstElementChild?.textContent?.trim() ?? '')
        .join(' · ')
      if (samples.at(-1) !== text)
        samples.push(text)
    }
  }
  const observer = new MutationObserver(inspect)
  observer.observe(document.documentElement, { childList: true, subtree: true, characterData: true, attributes: true })
  window.__nativeGenerationProbe = { samples, stop: () => observer.disconnect() }
  inspect()
}

async function generationReadings(context: ManagedNativeScenarioContext): Promise<GenerationCounters[]> {
  return (await context.page.evaluate(() => [...(window.__nativeGenerationProbe?.samples ?? [])])).map(parseGenerationCounters)
}

async function waitForIncreasingCounter(context: ManagedNativeScenarioContext, counter: 'tokens' | 'bytes', previous = 0): Promise<number> {
  let current = 0
  await expect.poll(async () => {
    const values = (await generationReadings(context)).flatMap((reading) => {
      const value = reading[counter]
      return value === undefined ? [] : [value]
    })
    current = values.length > 0 ? Math.max(...values) : 0
    return current
  }).toBeGreaterThan(previous)
  return current
}

/** A release of a held stream or output. The cleanup of a progress scenario runs it whether the scenario passes or not. */
type ProgressRelease = () => Promise<void>

/** What the turn of a progress scenario receives from the shared setup. */
interface ProgressTurnSetup {
  agent: AgentInfo
  /** Register a release before the hold that it ends. */
  onCleanup: (release: ProgressRelease) => void
}

/**
 * Run one native progress turn between the shared setup and the shared tail.
 *
 * `turn` runs the native turn until its last step arrives and returns a text that the completed transcript holds.
 * The tail waits for the native idle edge, requires that no counter showed for a provider without one, reloads, and
 * requires the completed text again.
 */
async function exerciseProgressTurn(
  context: ManagedNativeScenarioContext,
  options: NativeProgressCase,
  turn: (setup: ProgressTurnSetup) => Promise<string>,
): Promise<void> {
  await options.prepare?.()
  const agent = await currentNativeAgent(context)
  const after = await observeSettledReceipts(context.page)
  await context.page.evaluate(installGenerationObservation)
  const releases: ProgressRelease[] = []
  await withCleanup(async () => {
    const completedText = await turn({ agent, onCleanup: release => releases.push(release) })
    await waitForIdleSoundReceipt(context.page, { agentId: agent.id, after })
    await waitForAgentIdle(context.page)
    if (!options.supported) {
      const readings = await generationReadings(context)
      expect(readings.some(reading => reading.tokens !== undefined || reading.bytes !== undefined), 'the whole native turn exposes no live counter').toBe(false)
    }
    await context.page.reload()
    await expect(messageContents(context.page).filter({ hasText: completedText }).first()).toBeVisible()
  }, async () => {
    await finishCleanup([
      ...releases.map(release => release()),
      context.page.evaluate(() => window.__nativeGenerationProbe?.stop()),
    ])
  })
}

/**
 * Prove that the live token counter advances while a streamed answer holds after each of its first two chunks.
 * For a provider without a token counter (`supported: false`), prove that no counter shows for the whole turn.
 */
export async function exerciseTokenProgress(context: ManagedNativeScenarioContext, options: NativeTokenProgressCase): Promise<void> {
  await exerciseProgressTurn(context, options, async ({ onCleanup }) => {
    const suffix = uniqueMarker()
    const firstGate = `progress-first-${suffix}`
    const secondGate = `progress-second-${suffix}`
    onCleanup(async () => {
      for (const gate of [firstGate, secondGate])
        await context.modelScript.releaseGateIfHeld(gate)
    })
    const marker = `NATIVEPROGRESSTEXT${suffix}`
    const text = `${marker} records the actual streamed answer. `.repeat(8)
    const step = options.step ?? nativeTextStep(context, text)
    const start = await context.modelScript.queue({
      ...step,
      text,
      stream: { chunkChars: 24, delayMs: 0, gates: [{ afterChunk: 1, name: firstGate }, { afterChunk: 2, name: secondGate }] },
    })
    await sendMessage(context.page, context.modelScript.prompt('Stream the scripted answer, then complete the task.'))
    await context.modelScript.waitForGate(firstGate)
    const first = options.supported ? await waitForIncreasingCounter(context, 'tokens') : 0
    await context.modelScript.releaseGate(firstGate)
    await context.modelScript.waitForGate(secondGate)
    if (options.supported)
      await waitForIncreasingCounter(context, 'tokens', first)
    await context.modelScript.releaseGate(secondGate)
    await context.modelScript.waitForSteps(start + 1)
    await expect(assistantBubbles(context.page).filter({ hasText: marker }).first()).toBeVisible()
    return marker
  })
}

/** The answer that ends the turn of `exerciseOutputByteProgress`. */
const OUTPUT_PROGRESS_ANSWER = 'The native output scenario completed.'

/**
 * Prove that the live byte counter advances while a real shell command holds after each of its first two output
 * segments, and that the completed result holds both segments.
 * For a provider without a byte counter (`supported: false`), prove that no counter shows for the whole turn.
 */
export async function exerciseOutputByteProgress(context: ManagedNativeScenarioContext, options: NativeOutputByteProgressCase): Promise<void> {
  await exerciseProgressTurn(context, options, async ({ agent, onCleanup }) => {
    const output = createToolOutputControl(agent.workingDir, options.outputMarkers, { holdFirstOutput: options.waitForToolStart !== undefined })
    onCleanup(async () => {
      await output.releaseFirstOutput()
      await output.releaseFinalOutput()
    })
    const call = bashToolCall(context.provider, PROGRESS_OUTPUT_CALL_ID, output.command)
    const start = await context.modelScript.queue({ toolCalls: [call] }, nativeTextStep(context, OUTPUT_PROGRESS_ANSWER))
    await sendMessage(context.page, context.modelScript.prompt('Run the controlled output command and then complete.'))
    if (options.approveTool) {
      await context.modelScript.waitForSteps(start + 1)
      await waitForControlBanner(context.page)
      await answerControl(context.page, 'allow')
    }
    if (options.waitForToolStart) {
      await options.waitForToolStart()
      await output.releaseStartOutput()
    }
    await output.waitForFirstOutput()
    const first = options.supported ? await waitForIncreasingCounter(context, 'bytes') : 0
    await options.afterOutputBoundary?.({ ...outputBoundary(output), phase: 'first', count: first })
    await output.releaseFirstOutput()
    await output.waitForSecondOutput()
    const second = options.supported ? await waitForIncreasingCounter(context, 'bytes', first) : 0
    await options.afterOutputBoundary?.({ ...outputBoundary(output), phase: 'second', count: second })
    await output.releaseFinalOutput()
    await context.modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(context.page)
    await verifyCompletedGenerationOutput(call.id, { first: output.firstMarker, second: output.secondMarker }, {
      ...(options.prepareCompletedResultView ? { prepareView: options.prepareCompletedResultView } : {}),
      assertVisible: async marker => expect(messageContents(context.page).filter({ hasText: marker }).first()).toBeVisible(),
    })
    return OUTPUT_PROGRESS_ANSWER
  })
}
