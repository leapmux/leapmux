import type { ToolKind } from '../../ir/toolKind'

/**
 * The two built-in extensions whose tools this plugin reads by name.
 *
 * Not in `contracts/goose-protocol.json`, which holds the identifiers BOTH programs
 * read. The worker knows neither word, so the contract rule keeps them on the one side
 * that does. `summon` is the third built-in extension, and it IS in the contract
 * (`GOOSE_SUBAGENT`), because the worker reads it too.
 */
export const GOOSE_DEVELOPER_EXTENSION = 'developer'
export const GOOSE_TODO_EXTENSION = 'todo'

/** The one tool of the `todo` extension. */
export const GOOSE_TODO_TOOL = 'todo_write'

/**
 * The tool names of the `developer` extension, as the words a branch compares against.
 *
 * The kind table below keys on these, so one spelling serves both halves. A branch
 * that retyped the string kept compiling after a rename and simply stopped firing,
 * which the wire-token selectors in `eslint.config.ts` state the rule against: a provider's tokens live
 * as named tables so a call site reads a constant instead of retyping a string.
 */
export const GOOSE_DEVELOPER_TOOL = {
  Edit: 'edit',
  Read: 'read',
  ReadImage: 'read_image',
  Shell: 'shell',
  Tree: 'tree',
  Write: 'write',
} as const

/**
 * The kind of each tool Goose's `developer` extension runs.
 *
 * `as const satisfies` keeps every value its own literal, so a caller builds at
 * the kind the table states for the name it matched. A `Record<string, ToolKind>`
 * annotation widened them all to the union, which is what made each payload an
 * assertion.
 */
export const GOOSE_TOOL_KINDS = {
  [GOOSE_DEVELOPER_TOOL.Edit]: 'edit',
  [GOOSE_DEVELOPER_TOOL.Read]: 'read',
  [GOOSE_DEVELOPER_TOOL.ReadImage]: 'read',
  [GOOSE_DEVELOPER_TOOL.Shell]: 'execute',
  // The output is a tree with branch art and line counts, which the directory
  // listing states as the words it is.
  [GOOSE_DEVELOPER_TOOL.Tree]: 'list',
  [GOOSE_DEVELOPER_TOOL.Write]: 'write',
} as const satisfies Record<string, ToolKind>

/** Whether this tool belongs to the `developer` extension. A predicate, so the lookup keeps its literal. */
export function isGooseDeveloperTool(name: string): name is keyof typeof GOOSE_TOOL_KINDS {
  return Object.hasOwn(GOOSE_TOOL_KINDS, name)
}
