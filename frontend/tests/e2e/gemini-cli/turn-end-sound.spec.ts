import { geminiTest } from '../gemini-fixtures'
import { exerciseTurnEndSound } from '../helpers/nativeTurnEndSound'
import { bashToolCall } from '../helpers/providerToolCalls'
import { printfMarkerCommand } from '../helpers/shellArguments'
import { nativeContext } from './scenarios'

for (const toolActivity of [false, true]) {
  geminiTest(`uses the actual native activity when tool activity is ${toolActivity}`, async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
    const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
    await exerciseTurnEndSound(context, { toolActivity, ...(toolActivity ? { steps: [{ toolCalls: [bashToolCall(context.provider, 'gemini-sound-tool', printfMarkerCommand('GEMINISOUND', 42))] }, { text: 'The native tool sound turn completed.' }], approveTool: true } : {}) })
  })
}
