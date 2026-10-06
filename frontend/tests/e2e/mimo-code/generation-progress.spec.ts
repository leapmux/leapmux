import { exerciseTokenProgress } from '../helpers/generationProgress'
import { mimoTest } from '../mimo-fixtures'

mimoTest('proves the live native generation counter', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
