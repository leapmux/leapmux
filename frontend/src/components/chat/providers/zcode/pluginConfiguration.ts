import type { ProviderConfigurationCapability } from '../capabilities'
import { ZCODE_DEFAULT_MODE, ZCODE_MODE } from '~/generated/contracts/zcode-protocol'

/** The complete ZCode configuration channel, separate from registration. */
export const zcodeConfiguration: ProviderConfigurationCapability = {
  // Text is inlined into the prompt and images use session/send.attachments. The
  // app-server recognizes images, videos, files, and audio, but not PDF files. A
  // PDF would arrive as a generic file and become binary text or disappear when
  // too large, so LeapMux refuses it.
  attachments: { text: true, image: true, pdf: false, binary: false },
  triggerModeGroupKey: 'permissionMode',
  planMode: {
    groupKey: 'permissionMode',
    currentMode: agent => agent.optionValues?.permissionMode ?? ZCODE_DEFAULT_MODE,
    planValue: ZCODE_MODE.Plan,
    defaultValue: ZCODE_DEFAULT_MODE,
  },
}
