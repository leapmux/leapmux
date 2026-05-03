import type { ClassificationInput } from './registry'
import type { AvailableOption } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import {
  AvailableModelSchema,
  AvailableOptionGroupSchema,
  AvailableOptionSchema,
} from '~/generated/leapmux/v1/agent_pb'

/** Build a ClassificationInput from a parent object and optional wrapper, for tests. */
export function input(
  parent?: Record<string, unknown>,
  wrapper?: { old_seqs: number[], messages: unknown[] } | null,
): ClassificationInput {
  return {
    rawText: '',
    topLevel: parent ?? null,
    parentObject: parent,
    wrapper: wrapper ?? null,
  }
}

interface ModelOpts {
  isDefault?: boolean
  description?: string
  contextWindow?: bigint
  supportedEfforts?: { id: string, name: string, description?: string }[]
}

/** Build an AvailableModel for provider-settings tests. */
export function model(id: string, displayName: string, opts: ModelOpts = {}) {
  return create(AvailableModelSchema, { id, displayName, ...opts })
}

interface OptionOpts {
  isDefault?: boolean
  description?: string
}

/** Build an AvailableOption for provider-settings tests. */
export function option(id: string, name: string, opts: OptionOpts = {}) {
  return create(AvailableOptionSchema, { id, name, ...opts })
}

/** Build an AvailableOptionGroup for provider-settings tests. */
export function optionGroup(key: string, label: string, options: AvailableOption[]) {
  return create(AvailableOptionGroupSchema, { key, label, options })
}
