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
  extraSettings?: Record<string, string>
  availableModels?: AvailableModel[]
  availableOptionGroups?: AvailableOptionGroup[]
  onModelChange?: (model: string) => void
  onEffortChange?: (effort: string) => void
  onPermissionModeChange?: (mode: PermissionMode) => void
  onOptionGroupChange?: (key: string, value: string) => void
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
   * Returns true when the given control request payload represents an
   * "ask user question" interaction for this provider.
   */
  isAskUserQuestion?: (payload: Record<string, unknown>) => boolean

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

  /**
   * Plan mode toggle configuration. Providers define how plan mode
   * maps to their native settings so the shared toggle logic stays
   * provider-agnostic.
   */
  planMode?: {
    /** Read the current plan-relevant mode from agent state. */
    currentMode: (agent: { permissionMode?: string, extraSettings?: Record<string, string> }) => string
    /** The value that represents "plan" mode. */
    planValue: string
    /** The default (non-plan) mode value. */
    defaultValue: string
    /** Apply a mode change via the appropriate callback. */
    setMode: (mode: string, callbacks: {
      onPermissionModeChange?: (mode: PermissionMode) => void
      onOptionGroupChange?: (key: string, value: string) => void
    }) => void
  }

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
