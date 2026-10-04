import type { ProviderPermissionPresets } from '../../providerSettings'
import { GEMINI_MODE } from '~/generated/contracts/gemini-protocol'
import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'

/** Gemini's YOLO mode bypasses native permission prompts. */
export const geminiPermissionPresets = {
  bypass: { sets: { [OPTION_ID_PERMISSION_MODE]: GEMINI_MODE.Yolo } },
} satisfies ProviderPermissionPresets
