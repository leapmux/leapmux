import { exerciseTokenProgress } from '../helpers/generationProgress'
import { qwenTest } from '../qwen-fixtures'

qwenTest('proves the live native generation counter', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
