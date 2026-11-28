import { grokTest } from '../grok-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

grokTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
