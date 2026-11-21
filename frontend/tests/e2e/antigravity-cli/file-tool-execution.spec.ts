import { expect } from '@playwright/test'
import { ANTIGRAVITY_STEP_TYPE } from '../../../src/generated/contracts/antigravity-protocol'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { antigravityTest } from '../antigravity-fixtures'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'

antigravityTest('reads and changes actual native files and shows the applied diff', async ({ native }) => {
  await exerciseFileToolExecution(native)
  const { id } = await currentNativeAgent(native)
  const { messages } = await readNativeMessageSnapshot(native, id)
  const plannerSteps = messages.map(nativeMessageBody).filter(isObject).filter(step => step.type === ANTIGRAVITY_STEP_TYPE.PlannerResponse)
  expect(plannerSteps.length).toBeGreaterThan(0)
  expect(plannerSteps.filter((step) => {
    const response = pickObject(step, 'plannerResponse')
    return ['modifiedResponse', 'response', 'thinking'].every(key => !pickString(response, key).trim())
  })).toEqual([])
})
