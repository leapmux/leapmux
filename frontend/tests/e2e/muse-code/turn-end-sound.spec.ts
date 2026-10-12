import { exerciseTurnEndSound, nativeSoundReadTool } from '../helpers/nativeTurnEndSound'
import { museTest } from '../muse-fixtures'

museTest('plays the selected sound once after a native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { tool: await nativeSoundReadTool(native) })
})

museTest('keeps a text-only native turn quiet', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

museTest('keeps a native tool turn quiet when the selected sound is none', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: await nativeSoundReadTool(native) })
})
