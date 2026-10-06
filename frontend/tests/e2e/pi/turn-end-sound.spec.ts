import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { piTodoToolCall } from '../helpers/providerToolCalls'
import { piTest } from '../pi-fixtures'

piTest('plays the chosen sound once after a native tool turn', async ({ native }) => {
  await exerciseTurnEndSound(native, { tool: piTodoToolCall('sound-enabled-tool', { action: 'create', subject: 'Native sound activity' }) })
})

piTest('keeps a text-only native turn quiet', async ({ native }) => {
  await exerciseTurnEndSound(native)
})

piTest('plays no sound after a native tool turn when the chosen sound is none', async ({ native }) => {
  await exerciseTurnEndSound(native, { sound: 'none', tool: piTodoToolCall('sound-muted-tool', { action: 'create', subject: 'Native sound activity' }) })
})
