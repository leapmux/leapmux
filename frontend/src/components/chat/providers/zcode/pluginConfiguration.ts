import type { ProviderConfigurationCapability } from '../capabilities'
import { ZCODE_DEFAULT_MODE, ZCODE_MODE } from '~/generated/contracts/zcode-protocol'

/** The complete ZCode configuration channel, separate from registration. */
export const zcodeConfiguration: ProviderConfigurationCapability = {
  // Text is inlined into the prompt, and images and PDFs use session/send.attachments.
  // The worker refuses an image or a PDF that the current model declares no input
  // for. The app-server has no kind that keeps a binary file's bytes, so LeapMux
  // refuses a binary file.
  attachments: {
    text: true,
    image: true,
    pdf: true,
    audio: false,
    binary: false,
  },
  triggerModeGroupKey: 'permissionMode',
  planMode: {
    groupKey: 'permissionMode',
    currentMode: agent => agent.optionValues?.permissionMode ?? ZCODE_DEFAULT_MODE,
    planValue: ZCODE_MODE.Plan,
    defaultValue: ZCODE_DEFAULT_MODE,
  },
}
