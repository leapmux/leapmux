import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { updateTodosToolCall } from '../helpers/providerToolCalls'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('plays the chosen sound once after a native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { tool: updateTodosToolCall(native.provider, 'sound-enabled-tool', [{ step: 'Native sound activity', status: 'in_progress' }]) })
})

opencodeTest('keeps a text-only native turn quiet', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

opencodeTest('plays no sound after a native tool turn when the chosen sound is none', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: updateTodosToolCall(native.provider, 'sound-muted-tool', [{ step: 'Native sound activity', status: 'in_progress' }]) })
})
