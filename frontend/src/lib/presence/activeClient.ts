import { createSignal } from 'solid-js'

/**
 * activeClientStore is the per-(workspace_id) reactive map of the
 * currently-active client id, fed by PresenceUpdate events arriving
 * over the `/ws/orgevents` WebSocket subscription.
 * AppShell.handleTurnEnd consults this to gate the turn-end ding-dong:
 * only the client whose id matches the active client for the workspace
 * plays the sound.
 */
export function createActiveClientStore() {
  const [byWorkspace, setByWorkspace] = createSignal<Map<string, string>>(new Map())

  return {
    /** Active client id for `workspaceId`, or `''` when no clear leader. */
    activeFor(workspaceId: string): string {
      return byWorkspace().get(workspaceId) ?? ''
    },

    /** Apply a PresenceUpdate from the `/ws/orgevents` stream. */
    update(workspaceId: string, activeClientId: string): void {
      const prev = byWorkspace()
      const cur = prev.get(workspaceId) ?? ''
      if (cur === activeClientId)
        return
      const next = new Map(prev)
      if (activeClientId === '')
        next.delete(workspaceId)
      else
        next.set(workspaceId, activeClientId)
      setByWorkspace(next)
    },

    /** Drop every entry. Used on workspace logout / org switch. */
    clear(): void {
      setByWorkspace(new Map())
    },

    /** Reactive accessor for components that want to subscribe. */
    snapshot: byWorkspace,
  }
}

export type ActiveClientStore = ReturnType<typeof createActiveClientStore>
