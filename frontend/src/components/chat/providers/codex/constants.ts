import { OPTION_ID_PERMISSION_MODE } from '../../settingsGroups'

export const DEFAULT_CODEX_COLLABORATION_MODE = 'default'
export const DEFAULT_CODEX_SANDBOX_POLICY = 'workspace-write'
export const DEFAULT_CODEX_NETWORK_ACCESS = 'restricted'
export const DEFAULT_CODEX_SERVICE_TIER = 'default'
export const CODEX_OPTION_COLLABORATION_MODE = 'collaboration_mode'
export const CODEX_OPTION_SANDBOX_POLICY = 'sandbox_policy'
export const CODEX_OPTION_NETWORK_ACCESS = 'network_access'
export const CODEX_OPTION_SERVICE_TIER = 'service_tier'

/** The complete settings change that disables Codex permission prompts. */
export const CODEX_BYPASS_PERMISSION_SETTINGS: Readonly<Record<string, string>> = {
  [CODEX_OPTION_NETWORK_ACCESS]: 'enabled',
  [CODEX_OPTION_SANDBOX_POLICY]: 'danger-full-access',
  [OPTION_ID_PERMISSION_MODE]: 'never',
}
