import type { ProviderControlCapability } from '../capabilities'
import { kimiPermissionPresets } from './permissionPresets'

/** The Kimi Code control facts that the browser states, separate from provider registration. */
export const kimiControls: ProviderControlCapability = {
  // Smart runs routine edits and commands, then asks before risky calls. Bypass runs every call and decides every request itself.
  // Kimi calls these choices Ask When Needed and Never Ask.
  permissionPresets: kimiPermissionPresets,
}
