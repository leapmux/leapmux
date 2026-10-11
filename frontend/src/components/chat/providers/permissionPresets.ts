import type { ProviderPermissionPresets } from '../providerSettings'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { claudePermissionPresets } from './claude/permissionPresets'
import { codewhalePermissionPresets } from './codewhale/permissionPresets'
import { codexPermissionPresets } from './codex/permissionPresets'
import { copilotPermissionPresets } from './copilot/permissionPresets'
import { goosePermissionPresets } from './goose/permissionPresets'
import { kimiPermissionPresets } from './kimi/permissionPresets'
import { mimoPermissionPresets } from './mimo/permissionPresets'
import { reasonixPermissionPresets } from './reasonix/permissionPresets'
import { zcodePermissionPresets } from './zcode/permissionPresets'

/** List every provider explicitly. A new enum value requires a metadata decision. */
export const PROVIDER_PERMISSION_PRESETS = {
  [AgentProvider.UNSPECIFIED]: undefined,
  [AgentProvider.CLAUDE_CODE]: claudePermissionPresets,
  [AgentProvider.CODEX]: codexPermissionPresets,
  [AgentProvider.CURSOR]: undefined,
  [AgentProvider.GITHUB_COPILOT]: copilotPermissionPresets,
  [AgentProvider.KILO]: undefined,
  [AgentProvider.OPENCODE]: undefined,
  [AgentProvider.GOOSE]: goosePermissionPresets,
  [AgentProvider.PI]: undefined,
  [AgentProvider.REASONIX]: reasonixPermissionPresets,
  [AgentProvider.ZCODE]: zcodePermissionPresets,
  [AgentProvider.CODEWHALE]: codewhalePermissionPresets,
  [AgentProvider.KIMI_CODE]: kimiPermissionPresets,
  [AgentProvider.MIMO_CODE]: mimoPermissionPresets,
} satisfies Record<AgentProvider, ProviderPermissionPresets | undefined>

/** Read canonical preset data without importing a rendering plugin. */
export function permissionPresetsFor(provider: AgentProvider | undefined): ProviderPermissionPresets | undefined {
  return provider === undefined ? undefined : PROVIDER_PERMISSION_PRESETS[provider]
}
