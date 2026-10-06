import { cursorTest } from '../cursor-fixtures'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { updateTodosToolCall } from '../helpers/providerToolCalls'

// One Cursor turn is one Run exchange: the service streams the tool call and the answer text in one response
// (`helpers/cursorSurface.ts`). So the tool call and the answer form one step of a custom script.
cursorTest('plays a selected sound once for native tool activity and keeps text-only turns quiet', async ({ native }) => {
  await exerciseTurnEndSound(native, { steps: [{ toolCalls: [updateTodosToolCall(native.provider, 'sound-native-todo', [{ step: 'Native sound tool proof', status: 'pending' }])], text: 'The native sound tool turn ended.' }], prompt: 'Create the native sound to-do item.' })
  await exerciseTurnEndSound(native)
  await exerciseTurnEndSound(native, { sound: 'none', steps: [{ toolCalls: [updateTodosToolCall(native.provider, 'sound-muted-native-todo', [{ step: 'Muted native sound proof', status: 'pending' }])], text: 'The muted native tool turn ended.' }] })
})
