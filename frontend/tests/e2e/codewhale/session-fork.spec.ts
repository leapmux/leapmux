import { codewhaleTest } from '../codewhale-fixtures'
import { expectUnsupportedFork } from '../helpers/nativeFork'

codewhaleTest('reports supportsFork false and refuses native session forking', async ({ native }) => {
  await expectUnsupportedFork(native)
})
