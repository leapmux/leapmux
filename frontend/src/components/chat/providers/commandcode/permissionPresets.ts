import type { ProviderPermissionPresets } from '../../providerSettings'
import { COMMAND_CODE_PERMISSION_MODE } from '~/generated/contracts/commandcode-protocol'

/** The native bypass mode applies when the same session resumes in a new process. */
export const commandCodePermissionPresets = {
  bypass: { sets: { permissionMode: COMMAND_CODE_PERMISSION_MODE.Bypass } },
} satisfies ProviderPermissionPresets
