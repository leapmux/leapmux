import { geminiTest } from '../gemini-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

geminiTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
