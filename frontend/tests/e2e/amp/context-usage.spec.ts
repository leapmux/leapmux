import { ampTest } from '../amp-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

/**
 * The native usage event must reach the agent info card. The test checks the reported count and its display.
 *
 * The Worker drives Amp's stream JSON protocol. The isolated mock implements Amp's remote service.
 */
ampTest.describe('Amp basic chat', () => {
  ampTest('reports model usage in the agent info card', async ({ native }) => {
    await exerciseContextUsage(native)
  })
})
