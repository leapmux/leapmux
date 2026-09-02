/** One atomic change to one or more provider settings. */
export interface ProviderSettingChange {
  sets: Record<string, string>
}

export type ProviderSettingChangeHandler = (change: ProviderSettingChange) => void | Promise<void>

/** A provider action that applies several settings together. */
export interface ProviderSettingsAction {
  label: string
  testId: string
  sets: Record<string, string>
}
