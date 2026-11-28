import { codebuddyTest } from '../codebuddy-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

codebuddyTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
