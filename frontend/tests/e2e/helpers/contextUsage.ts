import type { Page } from '@playwright/test'
import type { MockModelUsage } from './mockModelScript'
import type { NativeScenarioContext } from './nativeScenario'
import { expect } from '@playwright/test'
import { formatTokenCount } from '../../../src/components/chat/rendererUtils'
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
 * The card's Context row prints `formatTokenCount(total) / window`. The total
 * is input, output and cache summed, so the marker is that one abbreviation,
 * not each count separately. The card states no per-count figure, and the
 * `context-usage-grid` test id belongs to the status-bar trigger's PipGrid,
 * which has no text nodes at all.
 */

/**
 * The combined token count the card prints for one scripted usage block.
 *
 * The mock reports input and output; the card sums them (plus any cache
 * counts) into one figure. A step that reports no count at all produces no
 * marker.
 */
function usageTotal(usage: MockModelUsage): number {
  return (usage.inputTokens ?? 0) + (usage.outputTokens ?? 0)
}

/**
 * The text markers a scripted usage block must show on the agent info card.
 *
 * One marker: the card prints the combined size, not the counts separately.
 */
export function usageMarkers(usage: MockModelUsage): string[] {
  if (usage.inputTokens === undefined && usage.outputTokens === undefined)
    return []
  return [formatTokenCount(usageTotal(usage))]
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
  return { tokens: parseTokenCount(match[1]!, match[2]!), window: parseTokenCount(match[3]!, match[4]!) }
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
export async function expectContextUsage(page: Page, usage: MockModelUsage): Promise<void> {
  const markers = usageMarkers(usage)
  if (markers.length === 0)
    throw new Error('The context usage check requires a usage block that states a token count.')
  const popover = await openAgentInfoCard(page)
  for (const marker of markers)
    await expect(popover).toContainText(marker)
  await page.keyboard.press('Escape')
}

/**
 * The usage block of the context usage scenario. 12000/40 is the marker of the module comment. The window feeds the
 * response shapes that state a window beside the usage.
 */
export const SCRIPTED_CONTEXT_USAGE: Readonly<MockModelUsage> = { inputTokens: 12_000, outputTokens: 40, contextWindow: 128_000 }

/**
 * Run one turn whose answer reports `SCRIPTED_CONTEXT_USAGE`, and require the agent info card to state it. With
 * `reload`, require it again after a reload, which reads the usage that the Worker stored. Return the usage.
 */
export async function exerciseContextUsage(context: NativeScenarioContext, options: { reload?: boolean } = {}): Promise<MockModelUsage> {
  const usage = { ...SCRIPTED_CONTEXT_USAGE }
  const step = await context.modelScript.queue({ ...nativeTextStep(context, 'Usage recorded.'), usage })
  await sendMessage(context.page, context.modelScript.prompt('Reply once.'))
  await context.modelScript.waitForSteps(step + 1)
  await waitForAgentIdle(context.page)
  await expectContextUsage(context.page, usage)
  if (options.reload) {
    await context.page.reload()
    await expectContextUsage(context.page, usage)
  }
  return usage
}
