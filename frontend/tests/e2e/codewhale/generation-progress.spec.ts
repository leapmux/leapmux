import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseTokenProgress } from '../helpers/generationProgress'

codewhaleTest('proves the live native generation counter', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
