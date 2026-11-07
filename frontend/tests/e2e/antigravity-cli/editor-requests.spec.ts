import { expect } from '@playwright/test'
import { antigravityExtractControl } from '../../../src/components/chat/providers/antigravity/extractControl'
import { antigravityTest } from '../antigravity-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'

antigravityTest('excludes the unsupported native editor control while a real permission still works', async ({ native }) => {
  const request = await sendNativeAnswer(native, 'Return the native control catalog.', 'The native control catalog reached the mock.')
  expect(nativeModelToolNames(request)).toContain('run_command')
  expect(nativeModelToolNames(request)).not.toContain('ask_user')
  await exerciseUnsupportedControlThroughPermission(native, { purpose: 'editor', classify: antigravityExtractControl })
})
