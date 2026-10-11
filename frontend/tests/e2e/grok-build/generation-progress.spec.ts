import { grokTest } from '../grok-fixtures'
import { exerciseTokenProgress } from '../helpers/generationProgress'

grokTest('proves the live native generation counter', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
