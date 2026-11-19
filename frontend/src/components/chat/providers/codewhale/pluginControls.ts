import type { ProviderControlCapability } from '../capabilities'
import { codewhaleControlResponseSummary } from './controlResponse'
import { codewhalePermissionPresets } from './permissionPresets'

/** The Codewhale control facts that the browser states, separate from provider registration. */
export const codewhaleControls: ProviderControlCapability = {
  controlResponseDisplay: codewhaleControlResponseSummary,
  permissionPresets: codewhalePermissionPresets,
}
