import type { Component, JSX } from 'solid-js'
import type { MessageCategory } from '../messageClassification'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { PermissionMode } from '~/utils/controlResponse'

export interface ProviderSettingsPanelProps {
  disabled?: boolean
  settingsLoading?: boolean
  model?: string
  effort?: string
  permissionMode?: string
  supportsModelEffort?: boolean
  onModelChange?: (model: string) => void
  onEffortChange?: (effort: string) => void
  onPermissionModeChange?: (mode: PermissionMode) => void
}

export interface ProviderPlugin {
  /** Classify a parsed message into a rendering category. */
  classify: (
    parent: Record<string, unknown> | undefined,
    wrapper: { old_seqs: number[], messages: unknown[] } | null,
  ) => MessageCategory

  /** Optional settings panel component for this provider's agent settings dropdown. */
  SettingsPanel?: Component<ProviderSettingsPanelProps>

  /** Optional trigger label renderer for the settings dropdown button. */
  settingsTriggerLabel?: (props: ProviderSettingsPanelProps) => JSX.Element
}

const registry = new Map<number, ProviderPlugin>()

export function registerProvider(provider: AgentProvider, plugin: ProviderPlugin): void {
  registry.set(provider, plugin)
}

export function getProviderPlugin(provider: AgentProvider): ProviderPlugin | undefined {
  return registry.get(provider)
}
