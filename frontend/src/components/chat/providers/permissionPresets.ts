import type { ProviderPermissionPresets } from '../providerSettings'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { ampPermissionPresets } from './amp/permissionPresets'
import { claudePermissionPresets } from './claude/permissionPresets'
import { clinePermissionPresets } from './cline/permissionPresets'
import { codebuddyPermissionPresets } from './codebuddy/permissionPresets'
import { codewhalePermissionPresets } from './codewhale/permissionPresets'
import { codexPermissionPresets } from './codex/permissionPresets'
import { commandCodePermissionPresets } from './commandcode/permissionPresets'
import { copilotPermissionPresets } from './copilot/permissionPresets'
import { deepseekHarnessPermissionPresets } from './deepseekharness/permissionPresets'
import { droidPermissionPresets } from './droid/permissionPresets'
import { geminiPermissionPresets } from './gemini/permissionPresets'
import { goosePermissionPresets } from './goose/permissionPresets'
import { grokPermissionPresets } from './grok/permissionPresets'
import { kimiPermissionPresets } from './kimi/permissionPresets'
import { kiroPermissionPresets } from './kiro/permissionPresets'
import { lettaPermissionPresets } from './letta/permissionPresets'
import { mimoPermissionPresets } from './mimo/permissionPresets'
import { musePermissionPresets } from './muse/permissionPresets'
import { ohMyPiPermissionPresets } from './ohmypi/permissionPresets'
import { qoderPermissionPresets } from './qoder/permissionPresets'
import { qwenPermissionPresets } from './qwen/permissionPresets'
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
  [AgentProvider.QWEN_CODE]: qwenPermissionPresets,
  [AgentProvider.OH_MY_PI]: ohMyPiPermissionPresets,
  [AgentProvider.GROK_BUILD]: grokPermissionPresets,
  [AgentProvider.KIRO]: kiroPermissionPresets,
  [AgentProvider.AMP]: ampPermissionPresets,
  [AgentProvider.CLINE]: clinePermissionPresets,
  [AgentProvider.CODEBUDDY]: codebuddyPermissionPresets,
  [AgentProvider.JUNIE]: undefined,
  [AgentProvider.LETTA]: lettaPermissionPresets,
  [AgentProvider.DIRAC]: undefined,
  [AgentProvider.QODER]: qoderPermissionPresets,
  [AgentProvider.DROID]: droidPermissionPresets,
  [AgentProvider.COMMAND_CODE]: commandCodePermissionPresets,
  [AgentProvider.DEEPSEEK_HARNESS]: deepseekHarnessPermissionPresets,
  [AgentProvider.GEMINI_CLI]: geminiPermissionPresets,
  [AgentProvider.MUSE_CODE]: musePermissionPresets,
  [AgentProvider.FAST_AGENT]: undefined,
} satisfies Record<AgentProvider, ProviderPermissionPresets | undefined>

/** Read canonical preset data without importing a rendering plugin. */
export function permissionPresetsFor(provider: AgentProvider | undefined): ProviderPermissionPresets | undefined {
  return provider === undefined ? undefined : PROVIDER_PERMISSION_PRESETS[provider]
}
