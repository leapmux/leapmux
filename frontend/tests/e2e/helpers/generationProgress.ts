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

export interface NativeGenerationProgressCase {
  supported: boolean
  counter?: 'tokens' | 'bytes'
  step?: MockModelStep
  prepare?: () => Promise<void>
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

/** Observe displayed counters before a native turn starts. The browser owns this state. */
export function installGenerationObservation(): void {
  window.__nativeGenerationProbe?.stop()
  const samples: string[] = []
  const inspect = () => {
    for (const element of document.querySelectorAll<HTMLElement>('[data-testid="thinking-indicator"]')) {
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

/** Prove live counter increases or inspect the complete native turn for an absent counter. */
export async function exerciseGenerationProgress(context: ManagedNativeScenarioContext, options: NativeGenerationProgressCase): Promise<void> {
  await options.prepare?.()
  const agent = await currentNativeAgent(context)
  const after = await observeSettledReceipts(context.page)
  await context.page.evaluate(installGenerationObservation)
  const counter = options.counter ?? 'tokens'
  let releaseOutput: (() => Promise<void>) | undefined
  const configuredGates: string[] = []
  await withCleanup(async () => {
    if (counter === 'bytes') {
      const holdFirstOutput = options.waitForToolStart !== undefined
      const output = createToolOutputControl(agent.workingDir, options.outputMarkers, { holdFirstOutput })
      releaseOutput = async () => {
        await output.releaseFirstOutput()
        await output.releaseFinalOutput()
      }
      const call = bashToolCall(context.provider, 'native-progress-output', output.command)
      const start = await context.modelScript.queue(
        { toolCalls: [call] },
        nativeTextStep(context, 'The native output scenario completed.'),
      )
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
      const first = options.supported ? await waitForIncreasingCounter(context, counter) : 0
      await options.afterOutputBoundary?.({ ...outputBoundary(output), phase: 'first', count: first })
      await output.releaseFirstOutput()
      await output.waitForSecondOutput()
      const second = options.supported ? await waitForIncreasingCounter(context, counter, first) : 0
      await options.afterOutputBoundary?.({ ...outputBoundary(output), phase: 'second', count: second })
      await output.releaseFinalOutput()
      await context.modelScript.waitForSteps(start + 2)
      await waitForAgentIdle(context.page)
      await verifyCompletedGenerationOutput(call.id, { first: output.firstMarker, second: output.secondMarker }, {
        ...(options.prepareCompletedResultView ? { prepareView: options.prepareCompletedResultView } : {}),
        assertVisible: async marker => expect(messageContents(context.page).filter({ hasText: marker }).first()).toBeVisible(),
      })
    }
    else {
      const suffix = uniqueMarker()
      const firstGate = `progress-first-${suffix}`
      const secondGate = `progress-second-${suffix}`
      configuredGates.push(firstGate, secondGate)
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
      const first = options.supported ? await waitForIncreasingCounter(context, counter) : 0
      await context.modelScript.releaseGate(firstGate)
      await context.modelScript.waitForGate(secondGate)
      if (options.supported)
        await waitForIncreasingCounter(context, counter, first)
      await context.modelScript.releaseGate(secondGate)
      await context.modelScript.waitForSteps(start + 1)
      await expect(assistantBubbles(context.page).filter({ hasText: marker }).first()).toBeVisible()
    }
    await waitForIdleSoundReceipt(context.page, { agentId: agent.id, after })
    await waitForAgentIdle(context.page)
    const readings = await generationReadings(context)
    if (!options.supported)
      expect(readings.some(reading => reading.tokens !== undefined || reading.bytes !== undefined), 'the whole native turn exposes no live counter').toBe(false)
    await context.page.reload()
    await expect(messageContents(context.page).filter({ hasText: counter === 'bytes' ? 'The native output scenario completed.' : 'NATIVEPROGRESSTEXT' }).first()).toBeVisible()
  }, async () => {
    const releaseStreams = async () => {
      for (const gate of configuredGates) {
        await context.modelScript.releaseGateIfHeld(gate)
      }
    }
    await finishCleanup([
      releaseStreams(),
      releaseOutput?.() ?? Promise.resolve(),
      context.page.evaluate(() => window.__nativeGenerationProbe?.stop()),
    ])
  })
}
