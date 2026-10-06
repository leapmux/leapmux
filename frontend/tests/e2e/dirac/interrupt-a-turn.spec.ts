import { diracTest } from '../dirac-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

diracTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

diracTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})
