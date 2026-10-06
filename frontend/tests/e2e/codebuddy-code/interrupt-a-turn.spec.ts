import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'

codebuddyTest('stops a native model turn and resumes its paused queue', async ({ native }) => {
  await exerciseInterruptTurn(native)
})

codebuddyTest('stops an actual native tool and accepts the next queued turn', async ({ native }) => {
  await exerciseInterruptTurn(native, { kind: 'tool' })
})
