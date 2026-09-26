import type { Page } from '@playwright/test'
import type { MockModelUsage } from './mockModelScript'
import { expect } from '@playwright/test'
import { formatTokenCount } from '../../../src/components/chat/rendererUtils'
import { openAgentInfoCard } from './ui'

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

/**
 * Open the agent info card and assert its Context row follows `usage`.
 */
export async function expectContextUsage(page: Page, usage: MockModelUsage): Promise<void> {
  const popover = await openAgentInfoCard(page)
  for (const marker of usageMarkers(usage))
    await expect(popover).toContainText(marker)
}
