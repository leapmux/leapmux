// LeapMux exposes no native multiline editor route for this provider.
// Muse asks its questions through the option-bearing user input request, and its
// approvals arrive as permission choices; neither is an editor request.
import { museControl } from '../../../src/components/chat/providers/muse/control'
import { MUSE_APPROVAL_MODE } from '../../../src/generated/contracts/muse-protocol'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { exerciseUnsupportedControlThroughPermission } from '../helpers/unsupportedNativeControl'
import { museTest } from '../muse-fixtures'

museTest('classifies real native controls and proves the missing editor-requests route', async ({ native }) => {
  // The fixture opens with every approval allowed; a real permission needs the ask mode.
  await chooseSettingsOption(native.page, `permissionMode-${MUSE_APPROVAL_MODE.PromptUnmatched}`)
  await waitForSettingsIdle(native.page)
  await exerciseUnsupportedControlThroughPermission(native, {
    purpose: 'editor',
    classify: input => museControl.extractControl?.(input) ?? null,
  })
})
