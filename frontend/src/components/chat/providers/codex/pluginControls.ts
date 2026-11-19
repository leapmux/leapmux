import type { ProviderControlCapability } from '../capabilities'
import { codexControlResponseSummary } from './controlResponse'
import { codexPermissionPresets } from './permissionPresets'

/** The Codex control facts that the browser states, separate from provider registration. */
export const codexControls: ProviderControlCapability = {
  permissionPresets: codexPermissionPresets,
  controlResponseDisplay: codexControlResponseSummary,
}
