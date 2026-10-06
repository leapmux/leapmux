import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { piTodoToolCall } from '../helpers/providerToolCalls'
import { piTest } from '../pi-fixtures'

piTest('plays a selected sound once for native tool activity and keeps text-only turns quiet', async ({ native }) => {
  await exerciseTurnEndSound(native, { tool: piTodoToolCall('sound-enabled-tool', { action: 'create', subject: 'Native sound activity' }) })
  await exerciseTurnEndSound(native)
  await exerciseTurnEndSound(native, { sound: 'none', tool: piTodoToolCall('sound-muted-tool', { action: 'create', subject: 'Native sound activity' }) })
})
