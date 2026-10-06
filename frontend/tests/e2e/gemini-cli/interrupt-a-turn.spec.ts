import { geminiTest } from '../gemini-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

for (const kind of ['model', 'tool'] as const) {
  geminiTest(`interrupts a held native ${kind} turn and keeps the session usable`, async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
    await exerciseInterruptTurn(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId }), { kind })
  })
}
