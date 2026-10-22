import type { ProviderPermissionPresets } from '../../providerSettings'
import { JUNIE_CONFIG } from '~/generated/contracts/junie-protocol'

/**
 * Junie owns these values for its Brave mode config option.
 * The worker forwards the values without interpretation, so this module owns them.
 * The shared contract supplies the option ID that both language consumers read.
 */
export const JUNIE_BRAVE = {
  Auto: 'brave-auto',
  On: 'on',
  Off: 'off',
} as const

/**
 * The canonical permission changes that this provider's presets apply.
 *
 * Smart selects Brave Auto. Brave Auto uses a safety check to classify terminal commands.
 * It approves the safe commands. It asks about the remaining commands.
 *
 * Bypass selects Brave On, which runs sensitive actions without approval.
 * Brave Off remains an ordinary option, like Claude Code's default mode.
 * The presets never change the plan/default mode axis.
 * Thus, Plan and a preset can remain active together.
 */
export const juniePermissionPresets = {
  smart: { sets: { [JUNIE_CONFIG.BraveMode]: JUNIE_BRAVE.Auto } },
  bypass: { sets: { [JUNIE_CONFIG.BraveMode]: JUNIE_BRAVE.On } },
} satisfies ProviderPermissionPresets
