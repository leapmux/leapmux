import { exerciseThinkingRows } from '../helpers/thinkingRows'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('uses Kimi Code for basic chat', () => {
  // The worker assembles the streamed reasoning and text into rows of its own,
  // so a reload reads the same rows that the live turn drew.
  kimiTest('draws the reasoning before the answer and keeps both after a reload', async ({ native }) => {
    await exerciseThinkingRows(native)
  })
})
