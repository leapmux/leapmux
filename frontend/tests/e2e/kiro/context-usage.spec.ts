import { expect } from '@playwright/test'
import { CONTEXT_USAGE_FIELD } from '../../../src/generated/contracts/session-info'
import { pickNumber } from '../../../src/lib/jsonPick'
import { SCRIPTED_CONTEXT_USAGE } from '../helpers/contextUsage'
import { watchAgentContextUsage } from '../helpers/contextUsageEvents'
import { selectedAgentTabId } from '../helpers/nativeScenario'
import { openAgentInfoCard, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'

/** The card's context line, which states its own numbers as "4.4k / 200.0k (2%)". */
const CONTEXT_LINE = /([\d.]+)([km]?)\s*\/\s*([\d.]+)([km]?)\s*\((\d+)%\)/i

/** Read a compact token count like "4.4k" as a plain number of tokens. */
function compactTokens(value: string, unit: string): number {
  const scale = unit.toLowerCase() === 'k'
    ? 1_000
    : unit.toLowerCase() === 'm'
      ? 1_000_000
      : 1
  return Math.round(Number.parseFloat(value) * scale)
}

kiroTest.describe('Kiro basic chat', () => {
  kiroTest('shows Kiro\'s native context percentage in the agent info card', async ({ native, leapmuxServer, page, modelScript }) => {
    const agentId = await selectedAgentTabId(native.page)
    const watch = await watchAgentContextUsage(leapmuxServer, agentId)
    try {
      const readings = () => watch.readings()
      const beforeCount = readings().length
      const step = await modelScript.queue({
        text: 'Usage recorded.',
        usage: { ...SCRIPTED_CONTEXT_USAGE },
      })
      await sendMessage(page, modelScript.prompt('Reply once.'))
      await modelScript.waitForSteps(step + 1)
      await waitForAgentIdle(page)
      await expect.poll(() => readings().length).toBeGreaterThan(beforeCount)
      // The card states the percentage that the reported tokens derive against
      // the window it shows: Kiro sums its usage breakdown, and the grid draws
      // the fill from those tokens.
      const reading = readings().at(-1)!
      const tokens = pickNumber(reading, CONTEXT_USAGE_FIELD.ContextTokens)
      expect(tokens ?? 0, 'the reading states the summed context tokens').toBeGreaterThan(0)
      if (tokens === null)
        throw new Error('The context reading stated no tokens.')
      const card = await openAgentInfoCard(page)
      const line = CONTEXT_LINE.exec(await card.textContent() ?? '')
      expect(line, 'the card states its tokens, its window, and the percentage').not.toBeNull()
      if (line === null)
        throw new Error('The agent info card stated no context line.')
      const shownTokens = compactTokens(line[1]!, line[2] ?? '')
      const shownWindow = compactTokens(line[3]!, line[4] ?? '')
      const percentText = line[5]!
      expect(shownTokens, 'the card states the reported tokens').toBeGreaterThanOrEqual(tokens - 500)
      expect(Number.parseInt(percentText, 10), 'the stated percentage derives from the tokens over the window')
        .toBe(Math.round((shownTokens / shownWindow) * 100))
    }
    finally {
      watch.cancel()
    }
  })
})
