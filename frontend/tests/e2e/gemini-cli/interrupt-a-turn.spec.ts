import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

for (const kind of ['model', 'tool'] as const) {
  geminiTest(`interrupts a held native ${kind} turn and keeps the session usable`, async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
    await exerciseInterruptTurn(nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId }), { kind })
  })
}
