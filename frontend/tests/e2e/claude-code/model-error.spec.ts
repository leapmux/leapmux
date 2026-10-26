import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest } from '../claude-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

claudeTest('reports a native model error and accepts the next turn', async ({ native }) => {
  // In auto mode, Claude Code 2.1.310 sends the dangerous-tool-use beta with a
  // `safeguards` field. It answers an HTTP 400 that it does not recognise with
  // the same retry ladder twice — with safeguards, with safeguards, without
  // them — so the failed turn sends six requests, and all must fail.
  const failed = await exerciseModelError(native, { queueAfterFailure: 'running', attempts: 6 })
  expect(isObject(failed[0]?.body) && Object.hasOwn(failed[0].body, 'safeguards')).toBe(true)
  expect(isObject(failed[2]?.body) && !Object.hasOwn(failed[2].body, 'safeguards')).toBe(true)
})
