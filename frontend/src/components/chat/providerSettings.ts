import type { PermissionMode } from '~/utils/controlResponse'

/** One atomic change to one or more provider settings. */
export interface ProviderSettingChange {
  sets: Record<string, string>
}

export type ProviderSettingChangeHandler = (change: ProviderSettingChange) => void | Promise<void>

/** The complete settings change that disables a provider's permission prompts. */
export interface ProviderBypassSettings extends ProviderSettingChange {
  sets: { permissionMode: PermissionMode } & Record<string, string>
}

/** A usable bypass action. The UI receives it only when both parts exist. */
export interface BypassController {
  settings: ProviderBypassSettings
  apply: ProviderSettingChangeHandler
}

/** A provider action that applies several settings together. */
export interface ProviderSettingsAction {
  label: string
  testId: string
  sets: Record<string, string>
}
