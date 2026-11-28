import { antigravityTest } from '../antigravity-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

antigravityTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
