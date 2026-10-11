import { exerciseTokenProgress } from '../helpers/generationProgress'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest('proves the live native generation counter', async ({ native }) => {
  await exerciseTokenProgress(native, { supported: true })
})
