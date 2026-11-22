import { readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { ANTIGRAVITY_STEP_TYPE } from '../../../src/generated/contracts/antigravity-protocol'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { antigravityTest } from '../antigravity-fixtures'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { exerciseFileToolExecution, runNativeToolSteps } from '../helpers/nativeToolExecution'
import { editToolCall } from '../helpers/providerToolCalls'
import { messageBubbles } from '../helpers/ui'

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

antigravityTest('shows landed line numbers and expandable context for a large file', async ({ native }) => {
  const { workingDir } = await currentNativeAgent(native)
  const path = join(workingDir, 'large-native-edit.txt')
  const before = 'original native line'
  const after = 'replacement native line'
  const prefix = Array.from({ length: 40 }, (_, index) => `unchanged prefix ${index + 1}`).join('\n')
  const suffix = Array.from({ length: 40 }, (_, index) => `unchanged suffix ${index + 1}`).join('\n')
  writeFileSync(path, `${prefix}\n${before}\n${suffix}\n`)
  const tool = editToolCall(native.provider, 'edit-large-file', { path, before, after })
  const step = {
    toolCalls: [tool],
  }
  await runNativeToolSteps(native, {
    steps: [step],
    prompt: 'Apply the scripted edit to the large file.',
    answer: 'The large file edit finished.',
  })
  expect(readFileSync(path, 'utf8')).toBe(`${prefix}\n${after}\n${suffix}\n`)
  const resultRows = messageBubbles(native.page).and(native.page.locator('[data-tool-row-role="result"]'))
  const diff = resultRows.locator('[data-file-diff]:visible').filter({ hasText: before }).filter({ hasText: after }).first()
  await expect(diff).toBeVisible()
  await expect(diff.getByText('41', { exact: true }).first()).toBeVisible()
  const gap = diff.getByText(/^\d+ lines hidden$/).first()
  await expect(gap).toBeVisible()
  await expect(diff.getByText('unchanged prefix 2', { exact: true })).toHaveCount(0)
  await gap.click()
  await expect(diff.getByText('unchanged prefix 2', { exact: true })).toBeVisible()
})
