import { grokTest } from '../grok-fixtures'
import { exerciseTurnEndSound, nativeSoundReadTool } from '../helpers/nativeTurnEndSound'

grokTest('plays the chosen sound once after a native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { tool: await nativeSoundReadTool(native) })
})

grokTest('keeps a text-only native turn quiet', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

grokTest('plays no sound after a native tool turn when the chosen sound is none', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: await nativeSoundReadTool(native) })
})
