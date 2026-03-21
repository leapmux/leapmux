import type { Component, JSX } from 'solid-js'
import type { ActionsProps, ContentProps } from '../controls/types'
import type { MessageCategory } from '../messageClassification'
import type { AgentProvider, AvailableModel, AvailableOptionGroup, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'

export interface ProviderSettingsPanelProps {
  disabled?: boolean
  settingsLoading?: boolean
  model?: string
  effort?: string
  permissionMode?: string
  codexCollaborationMode?: string
  codexSandboxPolicy?: string
  codexNetworkAccess?: string
  availableModels?: AvailableModel[]
  availableOptionGroups?: AvailableOptionGroup[]
  onModelChange?: (model: string) => void
  onEffortChange?: (effort: string) => void
  onPermissionModeChange?: (mode: PermissionMode) => void
  onCodexCollaborationModeChange?: (mode: string) => void
  onCodexSandboxPolicyChange?: (policy: string) => void
  onCodexNetworkAccessChange?: (access: string) => void
}

/** Context for rendering a message — forwarded from MessageBubble. */
export interface RenderContext {
  [key: string]: unknown
}

export interface ProviderPlugin {
  /** Default model identifier for this provider. */
  defaultModel?: string
  /** Default effort for this provider. */
  defaultEffort?: string
  /** Default permission mode identifier for this provider. */
  defaultPermissionMode?: PermissionMode

  /** Classify a parsed message into a rendering category. */
  classify: (
    parent: Record<string, unknown> | undefined,
    wrapper: { old_seqs: number[], messages: unknown[] } | null,
  ) => MessageCategory

  /**
   * Render a message given its category and parsed content.
   * Return null to fall through to the default renderer chain.
   */
  renderMessage?: (
    category: MessageCategory,
    parsed: unknown,
    role: MessageRole,
    context?: RenderContext,
  ) => JSX.Element | null

  /**
   * Build the wire-format content string to interrupt the agent.
   * Returns null if interrupt is not supported or not applicable.
   */
  buildInterruptContent?: (agentSessionId: string, codexTurnId?: string) => string | null

  /**
   * Build the wire-format content bytes to respond to an approval request.
   * The parsed Claude Code control_response is passed; the plugin translates
   * it to the provider's native format. Return null to send the original as-is.
   */
  buildControlResponse?: (parsed: Record<string, unknown>) => Uint8Array | null

  /**
   * The permission mode value that disables all approval prompts.
   * Used by the "& Bypass Permissions" button in approval controls.
   * E.g. "bypassPermissions" for Claude Code, "never" for Codex.
   */
  bypassPermissionMode?: PermissionMode

  /**
   * Change the agent's permission mode. Claude Code sends a lightweight
   * control_request (no restart), Codex restarts via UpdateAgentSettings.
   */
  changePermissionMode?: (
    workerId: string,
    agentId: string,
    mode: PermissionMode,
  ) => Promise<void>

  /** Change a provider-native collaboration mode, if supported. */
  changeCollaborationMode?: (
    workerId: string,
    agentId: string,
    mode: string,
  ) => Promise<void>

  /** Optional control request content component for this provider. */
  ControlContent?: Component<ContentProps>

  /** Optional control request actions component for this provider. */
  ControlActions?: Component<ActionsProps>

  /** Optional settings panel component for this provider's agent settings dropdown. */
  SettingsPanel?: Component<ProviderSettingsPanelProps>

  /** Optional trigger label renderer for the settings dropdown button. */
  settingsTriggerLabel?: (props: ProviderSettingsPanelProps) => JSX.Element

  /** Optional extra class for the settings dropdown menu container. */
  settingsMenuClass?: string
}

const registry = new Map<number, ProviderPlugin>()

export function registerProvider(provider: AgentProvider, plugin: ProviderPlugin): void {
  registry.set(provider, plugin)
}

export function getProviderPlugin(provider: AgentProvider): ProviderPlugin | undefined {
  return registry.get(provider)
}
