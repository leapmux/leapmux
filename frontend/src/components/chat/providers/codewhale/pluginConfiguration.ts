import type { ProviderConfigurationCapability } from '../capabilities'
import { CODEWHALE_DEFAULT_MODE, CODEWHALE_MODE, CODEWHALE_OPTION } from '~/generated/contracts/codewhale-protocol'

/** The complete Codewhale configuration channel, separate from registration. */
export const codewhaleConfiguration: ProviderConfigurationCapability = {
  // A turn takes text and inline images. Text is inlined into the prompt, and an image
  // rides in the turn's `images` list. The runtime has no file field, so a PDF or any
  // other binary is refused rather than sent as text it cannot read.
  attachments: { text: true, image: true, pdf: false, binary: false },
  // The mode axis is the thread's own mode, which LeapMux keeps apart from the
  // permission posture: Codewhale sets the two independently.
  triggerModeGroupKey: CODEWHALE_OPTION.Mode,
  planMode: {
    groupKey: CODEWHALE_OPTION.Mode,
    currentMode: agent => agent.optionValues?.[CODEWHALE_OPTION.Mode] ?? CODEWHALE_DEFAULT_MODE,
    planValue: CODEWHALE_MODE.Plan,
    defaultValue: CODEWHALE_DEFAULT_MODE,
  },
}
