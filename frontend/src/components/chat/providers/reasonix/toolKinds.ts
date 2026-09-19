import type { ToolKind } from '../../ir/toolKind'

/**
 * The tool names Reasonix states in a call's title, as the words a branch compares
 * against.
 *
 * The kind table below keys on these, so one spelling serves both halves. A branch
 * that retyped the string kept compiling after a rename and simply stopped firing,
 * which the wire-token selectors in `eslint.config.ts` state the rule against: a provider's tokens live
 * as named tables so a call site reads a constant instead of retyping a string.
 *
 * Not in `contracts/reasonix-protocol.json`, which holds the identifiers BOTH programs
 * read. The worker knows `task`, `read_only_task` and `use_capability` -- those three
 * are in the contract, as `REASONIX_TOOL` -- and none of the names below.
 */
export const REASONIX_TOOL_NAME = {
  Bash: 'bash',
  DeleteRange: 'delete_range',
  DeleteSymbol: 'delete_symbol',
  EditFile: 'edit_file',
  Glob: 'glob',
  Grep: 'grep',
  Ls: 'ls',
  MoveFile: 'move_file',
  MultiEdit: 'multi_edit',
  ReadFile: 'read_file',
  TodoWrite: 'todo_write',
  ViewImage: 'view_image',
  WebFetch: 'web_fetch',
  WriteFile: 'write_file',
} as const

/**
 * The kind of each tool Reasonix identifies by its title.
 *
 * `as const satisfies` rather than a `Record<string, ToolKind>` annotation: the
 * annotation widened every value to the whole union, so `REASONIX_TOOL_KINDS.glob` was "some
 * kind" and a branch that paired it with a search result had to assert the pair. The
 * const form keeps each value its own literal, and `satisfies` still refuses a word
 * that is not a `ToolKind`.
 *
 * `todo_write` is the one name above that this table omits: a checklist is not a tool
 * whose kind comes from the arguments, so the adapter builds it from its own branch.
 */
export const REASONIX_TOOL_KINDS = {
  [REASONIX_TOOL_NAME.Bash]: 'execute',
  [REASONIX_TOOL_NAME.DeleteRange]: 'delete',
  [REASONIX_TOOL_NAME.DeleteSymbol]: 'delete',
  [REASONIX_TOOL_NAME.EditFile]: 'edit',
  [REASONIX_TOOL_NAME.Glob]: 'glob',
  [REASONIX_TOOL_NAME.Grep]: 'grep',
  [REASONIX_TOOL_NAME.Ls]: 'list',
  [REASONIX_TOOL_NAME.MoveFile]: 'move',
  [REASONIX_TOOL_NAME.MultiEdit]: 'edit',
  [REASONIX_TOOL_NAME.ReadFile]: 'read',
  [REASONIX_TOOL_NAME.ViewImage]: 'read',
  [REASONIX_TOOL_NAME.WebFetch]: 'fetch',
  [REASONIX_TOOL_NAME.WriteFile]: 'write',
} as const satisfies Record<string, ToolKind>

/**
 * Whether Reasonix's own table lists this tool.
 *
 * A type PREDICATE, so a caller's lookup answers the tool's own literal kind
 * rather than the whole union. `Object.hasOwn` and not `??`, because `name` is the
 * frame's own title and one that spells an `Object.prototype` member is truthy -- the
 * wire kind would never win for a tool called `constructor`.
 */
export function isReasonixTool(name: string): name is keyof typeof REASONIX_TOOL_KINDS {
  return Object.hasOwn(REASONIX_TOOL_KINDS, name)
}
