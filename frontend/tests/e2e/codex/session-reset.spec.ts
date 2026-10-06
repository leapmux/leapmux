import { codexTest } from '../codex-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'

codexTest.describe('codex agent lifecycle', () => {
  codexTest('clear context via /clear command', async ({ native }) => {
    await exerciseSessionReset(native)
  })
})
