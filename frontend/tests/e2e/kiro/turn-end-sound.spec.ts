import { exerciseTurnEndSound, nativeSoundReadTool } from '../helpers/nativeTurnEndSound'
import { kiroTest } from '../kiro-fixtures'

kiroTest('plays the chosen sound once after a native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { tool: await nativeSoundReadTool(native) })
})

kiroTest('keeps a text-only native turn quiet', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

kiroTest('plays no sound after a native tool turn when the chosen sound is none', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: await nativeSoundReadTool(native) })
})
