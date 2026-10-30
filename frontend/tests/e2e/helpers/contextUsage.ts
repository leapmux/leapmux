import type { Page } from '@playwright/test'
import type { MockModelUsage } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { formatTokenCount } from '../../../src/components/chat/rendererUtils'
import { escapeRegExp } from '../../../src/lib/regexp'
import { resolveMockModelUsage } from './mockModelScript'
import { nativeTextStep } from './nativeScenario'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from './ui'

/**
 * Context usage on the agent info card.
 *
 * The mock scripts a `usage` block on a model step; the provider reads it and
 * the card prints it. `expectContextUsage` is the assertion that the card
 * followed the block the mock reported, and `usageMarkers` derives the text it
 * looks for. A default block of 1/1 makes every printed figure equal, so the
 * specs script 12000/40 as a marker: the combined figure then differs on
 * screen from a default run.
 *
 * The default reader assumes the card's Context row prints `formatTokenCount(total) / window`. The total
 * is the NORMALIZED breakdown summed: ordinary input (the cache-inclusive input
 * minus both cache buckets), both cache buckets, and output. A consistent
 * fixture therefore marks `input + output`; the cache buckets move the count
 * between the breakdown rows and cancel out of the sum. The marker is that one
 * abbreviation, not each count separately. The card states no per-count figure,
 * and the `context-usage-grid` test id belongs to the status-bar trigger's
 * PipGrid, which has no text nodes at all.
 */

/**
 * The combined token count the card prints for one scripted usage block.
 *
 * The mock reports a cache-INCLUSIVE input and the cache buckets inside it. The
 * card sums the normalized breakdown -- ordinary input plus both buckets plus
 * output -- so the total is `input + output` while the buckets fit inside the
 * input. An over-full breakdown clamps the ordinary input to zero, and the sum
 * then carries the whole cache traffic.
 */
function usageTotal(usage: MockModelUsage): number {
  const { inputTokens: input, outputTokens: output, cacheReadTokens: cacheRead, cacheWriteTokens: cacheWrite } = resolveMockModelUsage(usage)
  const ordinaryInput = Math.max(0, input - cacheRead - cacheWrite)
  return ordinaryInput + cacheRead + cacheWrite + output
}

/**
 * The text markers a scripted usage block must show on the agent info card.
 *
 * One marker: the card prints the combined size, not the counts separately.
 */
export function usageMarkers(usage: MockModelUsage, contextTokens: (usage: MockModelUsage) => number = usageTotal): string[] {
  if (usage.inputTokens === undefined && usage.outputTokens === undefined
    && usage.cacheReadTokens === undefined && usage.cacheWriteTokens === undefined) {
    return []
  }
  const tokens = contextTokens(usage)
  if (!Number.isFinite(tokens) || tokens < 0)
    throw new Error('The native context reader returned an invalid token count.')
  return [
    formatTokenCount(tokens),
  ]
}

/** The Context row of the agent info card, as token counts. */
export interface ContextRowReading {
  tokens: number
  window: number
}

const TOKEN_UNIT_SCALE: Readonly<Record<string, number>> = { '': 1, 'k': 1_000, 'M': 1_000_000 }

/**
 * Read a count that `formatTokenCount` wrote, such as `999`, `12.2k` or `1.0M`.
 *
 * The card rounds a count to one decimal of its unit. So the result is exact
 * to within 50 tokens for a `k` count, and to within 50,000 for an `M` count.
 */
function parseTokenCount(value: string, unit: string): number {
  const scale = TOKEN_UNIT_SCALE[unit]
  if (scale === undefined)
    throw new Error(`The Context row states an unknown unit: ${unit}`)
  return Math.round(Number(value) * scale)
}

/**
 * Read the Context row from the text of the agent info card.
 *
 * The row prints `formatTokenCount(total) / formatTokenCount(window)`. The
 * function returns undefined when the card has no such row. A provider that
 * reports only a percentage prints a row without counts, and that row also
 * returns undefined.
 */
export function parseContextRow(cardText: string): ContextRowReading | undefined {
  const match = /Context\s*(\d+(?:\.\d+)?)([kM]?)\s*\/\s*(\d+(?:\.\d+)?)([kM]?)/.exec(cardText)
  if (!match)
    return undefined
  return {
    tokens: parseTokenCount(match[1]!, match[2]!),
    window: parseTokenCount(match[3]!, match[4]!),
  }
}

/** Open the agent info card, read its Context row, and close the card. */
export async function readContextRow(page: Page): Promise<ContextRowReading | undefined> {
  const popover = await openAgentInfoCard(page)
  const text = await popover.textContent() ?? ''
  await page.keyboard.press('Escape')
  return parseContextRow(text)
}

/**
 * Open the agent info card, assert its Context row follows `usage`, and close the card, as `readContextRow` does.
 */
export async function expectContextUsage(page: Page, usage: MockModelUsage, contextTokens?: (usage: MockModelUsage) => number): Promise<void> {
  const markers = usageMarkers(usage, contextTokens)
  if (markers.length === 0)
    throw new Error('The context usage check requires a usage block that states a token count.')
  const popover = await openAgentInfoCard(page)
  for (const marker of markers)
    await expect(popover).toContainText(new RegExp(`Context\\s*${escapeRegExp(marker)}\\s*\\/`))
  await page.keyboard.press('Escape')
}

/**
 * The usage block of the context usage scenario. 12000/40 is the marker of the module comment. The window feeds the
 * response shapes that state a window beside the usage.
 */
export const SCRIPTED_CONTEXT_USAGE: Readonly<MockModelUsage> = { inputTokens: 12_000, outputTokens: 40, contextWindow: 128_000 }

/**
 * Providers can supply their native context-count reader through the scenario context.
 * Run one turn with distinct token counts. Require those counts in the live card and after reload.
 * The default uses SCRIPTED_CONTEXT_USAGE. A caller can supply another block to test a later update.
 */
export async function exerciseContextUsage(context: NativeScenarioContext, options: { usage?: MockModelUsage } = {}): Promise<MockModelUsage> {
  const usage = {
    ...(options.usage ?? SCRIPTED_CONTEXT_USAGE),
  }
  const step = await context.modelScript.queue({
    ...nativeTextStep(context, 'Usage recorded.'),
    usage,
  })
  await sendMessage(context.page, context.modelScript.prompt('Reply once.'))
  await context.modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(context.page)
  await expectContextUsage(context.page, usage, context.contextUsageTokens)
  await context.page.reload()
  await expectContextUsage(context.page, usage, context.contextUsageTokens)
  return usage
}
