import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest } from '../claude-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

claudeTest('reports a native model error and accepts the next turn', async ({ native }) => {
  // Claude Code 2.1.323 repeats its HTTP 400 fallback sequence twice in Auto mode.
  // Each sequence sends three safeguarded requests and one request without safeguards.
  const failed = await exerciseModelError(native, { queueAfterFailure: 'running', attempts: 8 })
  for (const index of [0, 1, 2, 4, 5, 6])
    expect(isObject(failed[index]?.body) && Object.hasOwn(failed[index].body, 'safeguards')).toBe(true)
  for (const index of [3, 7])
    expect(isObject(failed[index]?.body) && !Object.hasOwn(failed[index].body, 'safeguards')).toBe(true)
})
