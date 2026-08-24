import { KEY_ACTIVE_WORKSPACE, localStorageRemove, localStorageSet } from '~/lib/browserStorage'

export interface CreateWorkspaceSwitcherOpts {
  setActiveWorkspaceId: (next: string | null) => void
}

/**
 * Builds the one function allowed to change which workspace is active.
 *
 * It used to snapshot the outgoing workspace's whole tab + layout state into
 * the registry, in a strict order, because switching wiped the active store and
 * a late snapshot would capture the WRONG workspace's tabs. None of that
 * remains: every workspace's tabs are derived from the projection at all times,
 * so switching changes which slice the shell renders and nothing else.
 *
 * Terminal scrollback used to be flushed here too, because the outgoing
 * workspace's `TerminalView`s were about to unmount and dispose their xterm
 * instances. That capture now lives in `disposeTerminalInstance`, the teardown
 * chokepoint every disposal passes through — a switch is only ONE of the ways a
 * terminal's view goes away, and hanging the capture off this caller silently
 * lost the buffer for the others (a terminal tab moved to another workspace, a
 * floating window closed with a terminal in it).
 *
 * So what remains is two statements: flip the signal, then persist the new
 * selection so a reload reopens here. Persisting after the flip rather than
 * before means a throwing quota-exceeded write leaves the signal already
 * correct for this session and only loses the reload hint.
 *
 * The switcher used to take the user id and skip the write while it was empty,
 * because the key was templated by user. `browserStorage` now scopes every key
 * to the signed-in account, so there is no id to thread and no
 * not-signed-in-yet case to guard: the shell constructs this only inside
 * `AuthGuard`, where an account always exists.
 */
export function createWorkspaceSwitcher(opts: CreateWorkspaceSwitcherOpts): (next: string | null) => void {
  return (next: string | null) => {
    opts.setActiveWorkspaceId(next)

    if (next)
      localStorageSet(KEY_ACTIVE_WORKSPACE, next)
    else
      localStorageRemove(KEY_ACTIVE_WORKSPACE)
  }
}
