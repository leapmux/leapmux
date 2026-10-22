import type { ProviderPermissionPresets } from '../../providerSettings'

/**
 * The values of Junie's Brave mode config option (`brave_mode`), its own wire
 * vocabulary for the shell-safety axis. The worker forwards them unread, so
 * they live here and not in a contract.
 */
export const JUNIE_BRAVE = {
  Auto: 'brave-auto',
  On: 'on',
  Off: 'off',
} as const

/**
 * The canonical permission changes that this provider's presets apply.
 *
 * Smart selects Brave Auto, which classifies terminal commands with a safety
 * check, approves the safe ones, and asks about the rest. Bypass selects Brave
 * On, which runs sensitive actions without approval. Brave Off stays a plain
 * `brave_mode` option pick, like Claude Code's default mode. The presets never
 * touch the plan/default mode axis, so Plan and a preset can be on together.
 */
export const juniePermissionPresets = {
  smart: { sets: { brave_mode: JUNIE_BRAVE.Auto } },
  bypass: { sets: { brave_mode: JUNIE_BRAVE.On } },
} satisfies ProviderPermissionPresets
