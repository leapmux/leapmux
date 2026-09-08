import type { DetachedTerminal } from './tabView'
import type { AgentTab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import { createStore, produce } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { disposeTerminalInstance } from '~/components/terminal/TerminalView'
import { DEFAULT_TERMINAL_COLS, DEFAULT_TERMINAL_ROWS } from '~/lib/terminal'
import { openedTerminalMetadata, terminalMetadata } from '~/stores/tab.helpers'

/**
 * One agent tab's quake terminal: the shell that slides over the centre area.
 */
export interface QuakeEntry {
  /** The agent tab that owns this shell. */
  ownerId: string
  workerId: string
  workspaceId: string
  /**
   * The companion terminal, once the worker answered.
   *
   * ABSENT in the window between the first open and the RPC that resolves it,
   * which is what lets the panel animate while the worker is still answering.
   * The absence is in the type rather than an empty string, so a reader cannot
   * mistake "the RPC has not answered yet" for "an empty id", and so the three
   * call sites cannot each test it a different way.
   */
  terminalId?: string
  /** Whether the panel is visible. Per-client, and deliberately not persisted. */
  open: boolean
}

/** A resolved entry: one whose companion terminal the worker already gave. */
export type ResolvedQuakeEntry = QuakeEntry & { terminalId: string }

/** What the store needs from the shell to resolve and record a companion. */
export interface QuakeTerminalDeps {
  metadata: TabMetadataStore
  /**
   * The agent tab that owns a panel, by id. Read at call time rather than
   * captured, because the CLI command path gives an agent this store never
   * saw, and must resolve it against the live tab set.
   */
  getAgentTab: (agentId: string) => AgentTab | undefined
  /**
   * Restore keyboard focus to the composer when a panel closes.
   *
   * `terminalId` is the shell THIS close retracts, and it is the whole point of
   * the argument. One panel element holds every companion's terminal, so
   * "is focus inside the panel?" is true whenever ANY companion has the caret
   * -- and a background owner's close (its shell exited, or another device ran
   * the Control CLI) would then pull the caret out of the foreground shell the
   * user types in. The implementation compares this id against the focused
   * terminal instead.
   *
   * Called while the panel is still open, so focus is still where the user left
   * it: closing the panel marks it `inert`, which blurs whatever it holds.
   *
   * `terminalId` is absent when the RPC has not resolved yet, which is also
   * when no terminal of this entry can hold focus.
   */
  focusComposer?: (ownerId: string, terminalId: string | undefined) => void
  /** How long the panel takes to retract, so a dispose can wait it out. */
  closeDelayMs: () => number
  /**
   * Whether ONE workspace, by id, can be mutated -- false while it is archived,
   * and false for one that no longer exists.
   *
   * Consulted on the OPENING direction alone, and it lives here rather than in
   * each caller so the keyboard commands and the Control CLI cannot answer
   * differently. Per id, because a CLI request gives an agent in a workspace
   * this client does not display.
   */
  isWorkspaceMutatable: (workspaceId: string) => boolean
}

/**
 * The quake terminals this client knows about, and which panels are visible.
 *
 * NEVER stamp MRU on a quake terminal id. The MRU map is persisted, and
 * `useMetadataSweep` seeds its `seen` set from the persisted rows on the next
 * page load -- so one stamp makes the sweep retire a LIVE companion's metadata
 * row mid-session, taking its screen and cursor with it. A companion is not a
 * tab and is never selected, so nothing should be tempted to; this note is here
 * because the failure is silent and a page load away from its cause.
 *
 * The open state is per-client on purpose. The SHELL is shared -- one PTY per
 * agent tab, which every device attaches to -- but whether the panel is visible
 * is the same kind of fact as which tab is active in a tile, which this codebase
 * deliberately keeps client-local.
 */
export function createQuakeTerminalStore(deps: QuakeTerminalDeps) {
  const [entries, setEntries] = createStore<Record<string, QuakeEntry>>({})

  const entryFor = (ownerId: string): QuakeEntry | undefined => entries[ownerId]

  /**
   * Every panel this client holds whose shell is resolved.
   *
   * The watch plan reads this rather than mapping ids back through `ownerOf`:
   * it needs the owner and the open state together, and both are right here.
   */
  const liveEntries = (): ResolvedQuakeEntry[] =>
    Object.values(entries).filter((entry): entry is ResolvedQuakeEntry => entry.terminalId !== undefined)

  /** Every companion this client holds, for the tab view's detached family. */
  const detachedTerminals = (): DetachedTerminal[] => {
    const out: DetachedTerminal[] = []
    for (const entry of liveEntries())
      out.push({ id: entry.terminalId, workerId: entry.workerId, workspaceId: entry.workspaceId })
    return out
  }

  const ownerOf = (terminalId: string): string | undefined =>
    Object.values(entries).find(e => e.terminalId === terminalId)?.ownerId

  const isQuakeTerminal = (terminalId: string): boolean => ownerOf(terminalId) !== undefined

  /** Let go of one dead terminal's xterm instance and metadata row. */
  const releaseTerminal = (terminalId: string) => {
    disposeTerminalInstance(terminalId, { captureScreen: false })
    deps.metadata.remove(terminalId)
  }

  /** Forget one owner's entry. The store's only delete. */
  const dropEntry = (ownerId: string) => {
    setEntries(produce((state) => {
      delete state[ownerId]
    }))
  }

  /** Drop an entry and release everything it holds. Safe to run twice. */
  const release = (ownerId: string) => {
    const entry = entries[ownerId]
    if (!entry)
      return
    // `captureScreen: false` for the reason `handleTerminalClose` gives: the
    // terminal ends here, so a serialized buffer has no future reader.
    if (entry.terminalId !== undefined)
      releaseTerminal(entry.terminalId)
    dropEntry(ownerId)
  }

  /**
   * Abandon a panel whose open failed, and say so.
   *
   * No half-entry survives a failure: the next toggle must start clean rather
   * than show an empty panel for ever.
   */
  const failOpen = (ownerId: string, err: unknown) => {
    release(ownerId)
    showWarnToast('Failed to open the quake terminal', err)
  }

  /**
   * Resolve the companion for an owner: adopt the one the worker already has,
   * or ask it to spawn one.
   *
   * The list MUST precede the open. Inverting them would ask for a second shell
   * the worker's unique index then refuses, and -- worse -- a client that read
   * its own refusal as a failure would show an error for a panel that is
   * working. The list is also what makes a SECOND DEVICE attach to the first
   * device's shell rather than start its own.
   */
  const resolveTerminal = async (owner: AgentTab): Promise<void> => {
    const workerId = owner.workerId ?? ''
    const existing = await workerRpc.listTerminals(workerId, { tabIds: [], ownerAgentIds: [owner.id] })
    const adopted = existing.terminals.find(t => t.ownerAgentId === owner.id)
    if (adopted) {
      recordResolvedTerminal(owner.id, adopted.terminalId, () =>
        deps.metadata.patch(adopted.terminalId, terminalMetadata(workerId, adopted)))
      return
    }
    const resp = await workerRpc.openTerminal(workerId, {
      cols: DEFAULT_TERMINAL_COLS,
      rows: DEFAULT_TERMINAL_ROWS,
      workingDir: owner.workingDir ?? '',
      shell: '',
      workerId,
      shellStartDir: '',
      ownerAgentId: owner.id,
    })
    recordResolvedTerminal(owner.id, resp.terminalId, () =>
      deps.metadata.patch(resp.terminalId, openedTerminalMetadata({
        title: resp.title,
        workingDir: owner.workingDir ?? '',
      })))
  }

  /**
   * Attach a resolved terminal to its owner's entry, unless the entry is gone.
   *
   * The entry CAN be gone, because `resolveTerminal` awaits two RPCs and the
   * owner's tab can close in that window -- `retireOwners` then deletes the
   * key. A `setEntries(ownerId, 'terminalId', ...)` on a deleted key does not
   * no-op: Solid's `updatePath` dereferences the absent parent and THROWS, the
   * throw surfaces as a rejected promise, and the caller's catch shows "Failed
   * to open the quake terminal" for a tab the user merely closed.
   *
   * The shell the worker may have spawned in that window is left to the WORKER,
   * for the reason `retireOwners` gives: `closeAgentTabCommon` closes a
   * companion on every close path, and the orphan reconciler reaps one whose
   * owner the hub no longer lists. A CloseTerminal from here would be a second
   * teardown racing those.
   */
  function recordResolvedTerminal(ownerId: string, terminalId: string, applyMetadata: () => void) {
    if (entries[ownerId] === undefined)
      return
    applyMetadata()
    setEntries(ownerId, 'terminalId', terminalId)
  }

  const open = async (owner: AgentTab): Promise<void> => {
    const existing = entries[owner.id]
    if (existing) {
      // Already resolved, or resolving. Either way the panel just shows: no
      // RPC, which is what makes a toggle free and what keeps the shell alive
      // across one.
      if (!existing.open)
        setEntries(owner.id, 'open', true)
      return
    }
    if (!owner.workerId)
      return
    // Refused HERE, not at each call site, and only for a cold open: starting a
    // shell is a mutation of an archived workspace, and hiding a panel is not.
    // A guard on the whole toggle stranded a user whose workspace was archived
    // while the panel was up -- it covers the entire centre area and carries no
    // close control of its own.
    if (!deps.isWorkspaceMutatable(owner.workspaceId))
      return
    // The entry lands BEFORE the RPC, so the panel slides in while the worker
    // is still answering and shows the shell's own startup state. A panel that
    // appeared only after the round trip would jump into place on a cold open.
    // The panel itself owns the first frame -- see `armFirstSlide` in
    // `~/components/shell/QuakeTerminalPanel`.
    setEntries(owner.id, {
      ownerId: owner.id,
      workerId: owner.workerId,
      workspaceId: owner.workspaceId,
      open: true,
    })
    try {
      await resolveTerminal(owner)
    }
    catch (err) {
      failOpen(owner.id, err)
    }
  }

  const close = (ownerId: string) => {
    if (!entries[ownerId]?.open)
      return
    // Asked BEFORE the panel retracts, and the order is load-bearing. Closing
    // the panel marks it `inert`, which blurs whatever it holds -- so a
    // focus-restore that ran afterwards would see focus on the body and could
    // no longer tell "the user typed in the shell" from "the user closed
    // it from the transcript".
    deps.focusComposer?.(ownerId, entries[ownerId]?.terminalId)
    setEntries(ownerId, 'open', false)
  }

  const toggle = (owner: AgentTab): void => {
    if (entries[owner.id]?.open)
      close(owner.id)
    else
      void open(owner)
  }

  /**
   * Finish what `handleShellExit` started, once the panel has slid away.
   *
   * The user can toggle the panel back on DURING the retract, and that decides
   * which of two things happens here. It is a real window -- the animation is
   * 300 ms by default -- and getting it wrong strands the user in front of a
   * panel that unmounts itself a moment after they asked for it.
   */
  const finishShellExit = (ownerId: string, deadTerminalId: string) => {
    const entry = entries[ownerId]
    releaseTerminal(deadTerminalId)
    if (!entry || entry.terminalId !== deadTerminalId) {
      // Something already moved this owner on -- its tab closed, or a later
      // exit overtook this timer. The dispose above is all that was left.
      return
    }
    if (!entry.open) {
      dropEntry(ownerId)
      return
    }
    // Reopened during the retract. The panel stays, and it gets the fresh
    // shell that the user now waits for in front of an empty pane.
    setEntries(ownerId, 'terminalId', undefined)
    const owner = deps.getAgentTab(ownerId)
    // A fresh shell is a COLD open, so it takes the same refusal `open` applies
    // -- the workspace can have been archived while the panel was up, and the
    // reopen-during-retract window is precisely where that guard was missing.
    // Without it the client asks the worker for a shell in an archived
    // workspace and shows a failure toast for its own request.
    if (!owner || !deps.isWorkspaceMutatable(owner.workspaceId)) {
      dropEntry(ownerId)
      return
    }
    void resolveTerminal(owner).catch(err => failOpen(ownerId, err))
  }

  /**
   * The shell exited (the user typed `exit`, or it died).
   *
   * The panel RETRACTS with its usual slide before the instance is disposed, so
   * the user watches it leave instead of the content blanking underneath them.
   *
   * No CloseTerminal RPC: the worker closes a companion's row itself when its
   * shell exits, because a companion has no restart contract. The next open
   * therefore misses the companion lookup and spawns a fresh shell --
   * deliberately NOT the "press Enter to restart" a terminal TAB offers, since a
   * quake terminal the user ended should not leave a dead pane in front of them.
   */
  const handleShellExit = (terminalId: string) => {
    const ownerId = ownerOf(terminalId)
    if (ownerId === undefined)
      return
    // The same retract the user's own close performs, including the focus
    // restore it asks for first. `ownerOf` matched this entry on `terminalId`,
    // so `close` reports the identical shell.
    close(ownerId)
    const wait = deps.closeDelayMs()
    if (wait <= 0) {
      finishShellExit(ownerId, terminalId)
      return
    }
    setTimeout(finishShellExit, wait, ownerId, terminalId)
  }

  /**
   * Owner agent tabs that stopped being live, from the metadata sweep's
   * "was live, now is not" edge.
   *
   * Emits NO CRDT op: a companion has no record, and `applyTombstoneTab`
   * MATERIALIZES a record for an id it does not know -- so a tombstone here
   * would inject a phantom tab into the account. Issues no CloseTerminal
   * either: the worker's own `closeAgentTabCommon` already closed the companion
   * authoritatively, on every close path and from whichever device ran it, and
   * a second call from here would race that teardown.
   */
  const retireOwners = (retired: ReadonlySet<string>) => {
    for (const ownerId of retired)
      release(ownerId)
  }

  return {
    entryFor,
    liveEntries,
    detachedTerminals,
    ownerOf,
    isQuakeTerminal,
    open,
    close,
    toggle,
    handleShellExit,
    retireOwners,
  }
}

export type QuakeTerminalStore = ReturnType<typeof createQuakeTerminalStore>
