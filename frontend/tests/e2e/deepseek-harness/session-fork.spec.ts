import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

deepseekHarnessTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
