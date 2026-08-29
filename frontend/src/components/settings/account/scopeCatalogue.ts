import { Scope } from '~/generated/leapmux/v1/scope_pb'

/**
 * The grantable vocabulary as a person reads it: grouped the way scope.proto's
 * section comments group it, each scope with the sentence its proto comment
 * states.
 *
 * The ENUM is the vocabulary's source of truth and the catalogue is its
 * presentation, so the two are pinned together by test: a scope added to the
 * proto appears here only after somebody writes its sentence, and one removed
 * fails the suite rather than lingering as a checkbox that registers nothing.
 */
export interface ScopeEntry {
  scope: Scope
  description: string
}

/** One proto section: "Account", "Workspace", ... */
export interface ScopeCategory {
  label: string
  entries: ScopeEntry[]
}

export const SCOPE_CATEGORIES: readonly ScopeCategory[] = [
  {
    label: 'Account',
    entries: [
      {
        scope: Scope.ACCOUNT_READ,
        description: 'Read the signed-in account\'s profile: username, email, verification state, administrator flag.',
      },
      {
        scope: Scope.ACCOUNT_WRITE,
        description: 'Change the account\'s profile and preferences. No app reaches the credential surface.',
      },
    ],
  },
  {
    label: 'Workspace',
    entries: [
      {
        scope: Scope.WORKSPACE_READ,
        description: 'Read workspaces, tabs and the layout document.',
      },
      {
        scope: Scope.WORKSPACE_WRITE,
        description: 'Create, rename, move and close workspaces and tabs, and submit layout operations.',
      },
    ],
  },
  {
    label: 'Worker',
    entries: [
      {
        scope: Scope.WORKER_READ,
        description: 'List the account\'s workers and open a channel to one. Every other worker-surface scope implies it.',
      },
      {
        scope: Scope.WORKER_ADMIN,
        description: 'Administer the account\'s own workers: rename, deregister, and manage the registration keys that let a machine join.',
      },
    ],
  },
  {
    label: 'Agent',
    entries: [
      {
        scope: Scope.AGENT_READ,
        description: 'Read coding-agent sessions: messages, plans, settings, todos.',
      },
      {
        scope: Scope.AGENT_WRITE,
        description: 'Drive a coding agent: send a prompt, answer a permission request, start, stop, or change its settings.',
      },
    ],
  },
  {
    label: 'Terminal',
    entries: [
      {
        scope: Scope.TERMINAL_READ,
        description: 'Read terminal output and metadata. This is a monitoring dashboard.',
      },
      {
        scope: Scope.TERMINAL_WRITE,
        description: 'Type into a terminal. This is arbitrary code execution on the account\'s machine.',
      },
    ],
  },
  {
    label: 'File',
    entries: [
      {
        scope: Scope.FILE_READ,
        description: 'Browse directories and read files on a worker.',
      },
    ],
  },
  {
    label: 'Git',
    entries: [
      {
        scope: Scope.GIT_READ,
        description: 'Read git state: status, branches, diffs, log.',
      },
      {
        scope: Scope.GIT_WRITE,
        description: 'Mutate a repository: commit, push, create or delete a branch, manage a worktree.',
      },
    ],
  },
  {
    label: 'Tunnel',
    entries: [
      {
        scope: Scope.TUNNEL_OPEN,
        description: 'Open a TCP tunnel through a worker.',
      },
    ],
  },
  {
    label: 'Hub administration',
    entries: [
      {
        scope: Scope.ADMIN_READ,
        description: 'Read the hub\'s administrative inventory: users, settings, workers, sessions, credentials.',
      },
      {
        scope: Scope.ADMIN_USERS,
        description: 'Administer accounts: create, update, delete, reset a password, revoke sessions and credentials.',
      },
      {
        scope: Scope.ADMIN_SETTINGS,
        description: 'Change the hub\'s settings, including its security policy and its identity providers.',
      },
      {
        scope: Scope.ADMIN_WORKERS,
        description: 'Administer every worker on the hub, and the registration keys that admit one.',
      },
      {
        scope: Scope.ADMIN_APPS,
        description: 'Register, edit, vouch, retire and delete the hub\'s app registrations.',
      },
    ],
  },
]

/**
 * What a grant EXPANDS to: each key carries the scopes the hub adds to any
 * grant that includes it.
 *
 * This is the frontend's mirror of `impliedBy` in backend
 * internal/authscope, which expands a grant at the mint -- so a permission
 * ceiling stored without its implied scopes is closed by the hub before it is
 * stored (RegisterApp runs `scopes.Close()`), and the ceiling a consent screen
 * checks against is closed the same way. The form therefore shows the closure
 * the hub will perform: an implied scope renders checked and disabled, and
 * the submitted set carries it, so what the owner ticked is exactly what the
 * next ListApps reads back. The mirror is pinned to the backend table by
 * TestFrontendImpliedByMatchesTheHubGraph, so the two cannot drift.
 *
 * Three families, each a case where granting one scope without the other
 * would promise something the hub cannot deliver: every worker-surface scope
 * needs the channel worker:read opens; every write implies its own read; and
 * every admin:* scope implies admin:read, because administering a thing
 * starts with listing it.
 */
const IMPLIED_BY: Readonly<Partial<Record<Scope, readonly Scope[]>>> = {
  [Scope.ACCOUNT_WRITE]: [Scope.ACCOUNT_READ],
  [Scope.WORKSPACE_WRITE]: [Scope.WORKSPACE_READ],
  [Scope.WORKER_ADMIN]: [Scope.WORKER_READ],
  [Scope.AGENT_READ]: [Scope.WORKER_READ],
  [Scope.AGENT_WRITE]: [Scope.WORKER_READ, Scope.AGENT_READ],
  [Scope.TERMINAL_READ]: [Scope.WORKER_READ],
  [Scope.TERMINAL_WRITE]: [Scope.WORKER_READ, Scope.TERMINAL_READ],
  [Scope.FILE_READ]: [Scope.WORKER_READ],
  [Scope.GIT_READ]: [Scope.WORKER_READ],
  [Scope.GIT_WRITE]: [Scope.WORKER_READ, Scope.GIT_READ],
  [Scope.TUNNEL_OPEN]: [Scope.WORKER_READ],
  [Scope.ADMIN_USERS]: [Scope.ADMIN_READ],
  [Scope.ADMIN_SETTINGS]: [Scope.ADMIN_READ],
  [Scope.ADMIN_WORKERS]: [Scope.ADMIN_READ],
  [Scope.ADMIN_APPS]: [Scope.ADMIN_READ],
}

// Object.entries(IMPLIED_BY) returns numeric enum keys as STRINGS, and a Set
// of numbers would never match one -- so each key is parsed once, here,
// rather than on every walk of the table.
const IMPLIED_BY_ENTRIES = Object.entries(IMPLIED_BY)
  .map(([key, implied]) => [Number(key) as Scope, implied] as const)

/**
 * The closure of `selected`: the ticked scopes plus everything they imply,
 * transitively -- the same fixed point the hub's ScopeSet.Close computes,
 * iterated rather than assumed one pass deep so a future two-step
 * implication is correct with no edit here.
 *
 * The one primitive both derived views read: what the form LOCKS is this
 * closure minus the ticked set, and what it SUBMITS is the closure itself.
 */
function closure(selected: readonly Scope[]): Set<Scope> {
  const closed = new Set(selected)
  for (let added = true; added;) {
    added = false
    for (const [scope, implied] of IMPLIED_BY_ENTRIES) {
      if (!closed.has(scope))
        continue
      for (const target of implied) {
        if (!closed.has(target)) {
          closed.add(target)
          added = true
        }
      }
    }
  }
  return closed
}

/**
 * The scopes `selected` implies: the closure minus the ticked scopes
 * themselves.
 */
export function impliedScopes(selected: readonly Scope[]): Set<Scope> {
  const closed = closure(selected)
  for (const scope of selected)
    closed.delete(scope)
  return closed
}

/**
 * The set to SUBMIT: what was ticked, plus what the hub would add anyway.
 *
 * Sorted in enum order, the canonical order the hub sorts by, so the request
 * reads the same as the stored ceiling it becomes.
 */
export function closeScopes(selected: readonly Scope[]): Scope[] {
  return [...closure(selected)].sort((a, b) => a - b)
}
