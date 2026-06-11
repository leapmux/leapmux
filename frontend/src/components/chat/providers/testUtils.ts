import type { ClassificationInput } from './registry'
import type { AvailableOption } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import {
  AgentProvider,
  AvailableOptionGroupSchema,
  AvailableOptionSchema,
} from '~/generated/leapmux/v1/agent_pb'

/**
 * Build a ClassificationInput from a parent object and optional wrapper, for
 * tests. `agentProvider` defaults to CLAUDE_CODE so `classifyMessage(input(...))`
 * dispatches to the Claude plugin (now that classifyMessage no longer falls back
 * to Claude for an unset provider); pass an explicit provider to exercise another
 * plugin's dispatch or the `unsupported_provider` path.
 */
export function input(
  parent?: Record<string, unknown>,
  wrapper?: { old_seqs: number[], messages: unknown[] } | null,
  agentProvider: AgentProvider = AgentProvider.CLAUDE_CODE,
): ClassificationInput {
  return {
    rawText: '',
    topLevel: parent ?? null,
    parentObject: parent,
    wrapper: wrapper ?? null,
    agentProvider,
  }
}

interface ModelOpts {
  description?: string
  contextWindow?: bigint
}

/**
 * Build a model option for provider-settings tests. Models are now ordinary
 * options inside the "model" option group (the standalone AvailableModel message
 * was removed), so this constructs an AvailableOption with `name` set from the
 * display name.
 */
export function model(id: string, displayName: string, opts: ModelOpts = {}) {
  return create(AvailableOptionSchema, { id, name: displayName, ...opts })
}

interface OptionOpts {
  description?: string
}

/** Build an AvailableOption for provider-settings tests. */
export function option(id: string, name: string, opts: OptionOpts = {}) {
  return create(AvailableOptionSchema, { id, name, ...opts })
}

/** Build an AvailableOptionGroup for provider-settings tests. */
export function optionGroup(id: string, label: string, options: AvailableOption[]) {
  return create(AvailableOptionGroupSchema, { id, label, options })
}
