import type { ClassificationInput } from './registry'
import { MessageRole } from '~/generated/leapmux/v1/agent_pb'

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
    messageRole: MessageRole.SYSTEM,
  }
}
