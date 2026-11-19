import type { ProviderControlCapability } from '../capabilities'
import { codewhalePermissionPresets } from './permissionPresets'

/** The Codewhale control facts that the browser states, separate from provider registration. */
export const codewhaleControls: ProviderControlCapability = {
  permissionPresets: codewhalePermissionPresets,
}
