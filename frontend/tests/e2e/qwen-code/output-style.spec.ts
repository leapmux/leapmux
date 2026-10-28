import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { qwenTest } from '../qwen-fixtures'

/**
 * Prove the style reached the native request: Qwen appends the section
 * `# Output Style: <name>` to the model's system prompt (0.26.0). Inspect the
 * instruction messages only, so conversation text cannot supply the marker.
 */
function expectNativeStyle(request: MockModelRequestRecord, style: string) {
  const text = JSON.stringify(request.body ?? {})
  expect(text.includes(`# Output Style: ${style}`), `the native request carries the ${style} output style`).toBe(true)
}

qwenTest('applies the native output style and restores it after a reload', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'outputStyle',
    value: 'Concise',
    nativeProof: request => expectNativeStyle(request, 'Concise'),
  })
})

// Track the native route and its LeapMux integration: https://github.com/leapmux/leapmux/issues/508.
