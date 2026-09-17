import type { ToolCallIR, ToolCallOf } from '../../ir/toolCall'
import type { ToolKind } from '../../ir/toolKind'
import type { ParsedCall, ResolvedCall, ToolKindRenderer } from './renderer'
import { agentRenderer } from './agent'
import { agentsRenderer } from './agents'
import { chartRenderer } from './chart'
import { deleteRenderer } from './delete'
import { editRenderer } from './edit'
import { executeRenderer } from './execute'
import { fetchRenderer } from './fetch'
import { globRenderer } from './glob'
import { grepRenderer } from './grep'
import { imageRenderer } from './image'
import { listRenderer } from './list'
import { mcpRenderer } from './mcp'
import { memoryRenderer } from './memory'
import { messageRenderer } from './message'
import { moveRenderer } from './move'
import { noneRenderer } from './none'
import { otherRenderer } from './other'
import { questionRenderer } from './question'
import { readRenderer } from './read'
import { parsedCall, resolvedCall } from './renderer'
import { reportRenderer } from './report'
import { searchRenderer } from './search'
import { skillRenderer } from './skill'
import { switchModeRenderer } from './switchMode'
import { taskRenderer } from './task'
import { thinkRenderer } from './think'
import { todoRenderer } from './todo'
import { triggerRenderer } from './trigger'
import { waitRenderer } from './wait'
import { webSearchRenderer } from './webSearch'
import { writeRenderer } from './write'

/** The one renderer per kind. Total by mapped type: a missing kind is a compile error. */
export const TOOL_KIND_RENDERERS: { readonly [K in ToolKind]: ToolKindRenderer<K> } = {
  '': noneRenderer,
  'agent': agentRenderer,
  'agents': agentsRenderer,
  'chart': chartRenderer,
  'delete': deleteRenderer,
  'edit': editRenderer,
  'execute': executeRenderer,
  'fetch': fetchRenderer,
  'glob': globRenderer,
  'grep': grepRenderer,
  'image': imageRenderer,
  'list': listRenderer,
  'mcp': mcpRenderer,
  'memory': memoryRenderer,
  'message': messageRenderer,
  'move': moveRenderer,
  'other': otherRenderer,
  'question': questionRenderer,
  'read': readRenderer,
  'report': reportRenderer,
  'search': searchRenderer,
  'skill': skillRenderer,
  'switch_mode': switchModeRenderer,
  'task': taskRenderer,
  'think': thinkRenderer,
  'todo': todoRenderer,
  'trigger': triggerRenderer,
  'wait': waitRenderer,
  'web_search': webSearchRenderer,
  'write': writeRenderer,
}

/** The one cast in the layer: the table is total and keyed by the same `kind` the call carries. */
export function rendererFor<K extends ToolKind>(call: { kind: K }): ToolKindRenderer<K> {
  return TOOL_KIND_RENDERERS[call.kind] as ToolKindRenderer<K>
}

/** One renderer beside the views of the call its hooks read, all of ONE kind. */
export interface ToolCallDispatch<K extends ToolKind> {
  renderer: ToolKindRenderer<K>
  call: ToolCallOf<K>
  /** The call with the failed and unparsed brands stripped from its result slot. */
  parsed: ParsedCall<K>
  /** The parsed call when its result slot holds the kind's own payload; undefined when the slot is empty. */
  resolved: ResolvedCall<K> | undefined
}

/**
 * The renderer of one call's own kind, beside the views of the call its hooks read.
 *
 * Every member carries the SAME kind: `rendererFor` selects over the total table, and
 * the three views are built from the call itself. No assertion is stated here -- the
 * table's mapped type keys each renderer by the same literal the call holds, and the
 * union a real call arrives as satisfies every member at its own kind.
 */
export function dispatchParts(call: ToolCallIR): ToolCallDispatch<ToolKind> {
  return {
    renderer: rendererFor(call),
    call,
    parsed: parsedCall(call),
    resolved: resolvedCall(call),
  }
}

/**
 * Hand one call to the renderer of its own kind, with every view a hook reads.
 *
 * `ToolKindRenderer` declares its hooks as property functions, and several are
 * OPTIONAL -- so their parameters are contravariant in the kind, and no caller can
 * name a type that pairs a call with a renderer over every kind at once. Before this
 * helper existed, each call site re-stated the pairing with an assertion
 * (`parsed as ResolvedCall<ToolKind>`), and the assertion was the only thing that
 * kept a mismatched pair off the screen.
 *
 * Here the pairing is made ONCE: {@link dispatchParts} selects over the total table
 * and builds the parts the hooks declare, so `op` receives a correlated set and no
 * assertion exists outside this table module. `toolCallIrIsUnasserted.test.ts` holds
 * that rule.
 */
export function dispatchToolCall<R>(call: ToolCallIR, op: <K extends ToolKind>(parts: ToolCallDispatch<K>) => R): R {
  return op(dispatchParts(call))
}
