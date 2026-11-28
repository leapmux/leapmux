import { expectUnsupportedFork } from '../helpers/nativeFork'
import { qwenTest } from '../qwen-fixtures'

qwenTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
