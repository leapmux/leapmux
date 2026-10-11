import { expect } from '@playwright/test'
import { acpExtractControl } from '../../../src/components/chat/providers/acp/extractControl'
import { gooseTest } from '../goose-fixtures'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'

gooseTest('offers an actual native permission without an agent question sequence', async ({ native }) => {
  await chooseSettingsOption(native.page, 'permissionMode-approve')
  await waitForSettingsIdle(native.page)
  await exerciseUnsupportedControlThroughPermission(native, {
    purpose: 'question',
    classify: acpExtractControl,
    // The catalog that the model read offers no question tool of any known spelling.
    nativeProof: (request) => {
      const tools = nativeModelToolNames(request)
      expect(tools.length).toBeGreaterThan(0)
      expect(tools.some(tool => /^(?:ask_user|ask_user_question|AskUserQuestion|__human_input)$/.test(tool))).toBe(false)
    },
  })
})
