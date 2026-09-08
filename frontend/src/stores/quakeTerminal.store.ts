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
 *
 * `terminalId` is empty only in the window between the first open and the RPC
 * that resolves it, which is what lets the panel animate while the worker is
 * still answering.
 */
export interface QuakeEntry {
  /** The agent tab that owns this shell. */
  ownerId: string
  workerId: string
  workspaceId: string
  terminalId: string
  /** Whether the panel is showing. Per-client, and deliberately not persisted. */
  open: boolean
}

/** What the store needs from the shell to resolve and record a companion. */
export interface QuakeTerminalDeps {
  metadata: TabMetadataStore
  /**
   * The agent tab that owns a panel, by id. Read at call time rather than
   * captured, because the CLI command path names an agent this store has never
   * seen and must resolve it against the live tab set.
   */
  getAgentTab: (agentId: string) => AgentTab | undefined
  /**
   * Restore keyboard focus to the composer when a panel closes.
   *
   * Called while the panel is still open, so the implementation can ask whether
   * focus is inside it -- closing by shortcut from the transcript, or from
   * another device through the Control CLI, must not yank the caret out of
   * wherever the user is working.
   */
  focusComposer?: (ownerId: string) => void
  /** How long the panel takes to retract, so a dispose can wait it out. */
  closeDelayMs: () => number
  /**
   * Whether one named workspace can be mutated -- false while it is archived,
   * and false for one that no longer exists.
   *
   * Consulted on the OPENING direction alone, and it lives here rather than in
   * each caller so the keyboard commands and the Control CLI cannot answer
   * differently. Per id, because a CLI request names an agent in a workspace
   * this client may not be looking at.
   */
  isWorkspaceMutatable: (workspaceId: string) => boolean
}

/**
 * The quake terminals this client knows about, and which panels are showing.
 *
 * NEVER stamp MRU on a quake terminal id. The MRU map is persisted, and
 * `useMetadataSweep` seeds its `seen` set from the persisted rows on the next
 * page load -- so one stamp makes the sweep retire a LIVE companion's metadata
 * row mid-session, taking its screen and cursor with it. A companion is not a
 * tab and is never selected, so nothing should be tempted to; this note is here
 * because the failure is silent and a page load away from its cause.
 *
 * The open state is per-client on purpose. The SHELL is shared -- one PTY per
 * agent tab, which every device attaches to -- but whether the panel is showing
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
  const liveEntries = (): QuakeEntry[] =>
    Object.values(entries).filter(entry => entry.terminalId !== '')

  /** Every companion this client holds, for the tab view's detached family. */
  const detachedTerminals = (): DetachedTerminal[] => {
    const out: DetachedTerminal[] = []
    for (const entry of Object.values(entries)) {
      if (entry.terminalId)
        out.push({ id: entry.terminalId, workerId: entry.workerId, workspaceId: entry.workspaceId })
    }
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

  /** Drop an entry and release everything it holds. Safe to run twice. */
  const release = (ownerId: string) => {
    const entry = entries[ownerId]
    if (!entry)
      return
    // `captureScreen: false` for the reason `handleTerminalClose` gives: the
    // terminal is going away, so a serialized buffer has no future reader.
    if (entry.terminalId)
      releaseTerminal(entry.terminalId)
    setEntries(produce((state) => {
      delete state[ownerId]
    }))
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
      deps.metadata.patch(adopted.terminalId, terminalMetadata(workerId, adopted))
      setEntries(owner.id, 'terminalId', adopted.terminalId)
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
    deps.metadata.patch(resp.terminalId, openedTerminalMetadata({
      title: resp.title,
      workingDir: owner.workingDir ?? '',
    }))
    setEntries(owner.id, 'terminalId', resp.terminalId)
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
      terminalId: '',
      open: true,
    })
    try {
      await resolveTerminal(owner)
    }
    catch (err) {
      // No half-entry survives a failure: the next toggle must start clean
      // rather than show an empty panel forever.
      release(owner.id)
      showWarnToast('Failed to open the quake terminal', err)
    }
  }

  const close = (ownerId: string) => {
    if (!entries[ownerId]?.open)
      return
    // Asked BEFORE the panel retracts, and the order is load-bearing. Closing
    // the panel marks it `inert`, which blurs whatever it holds -- so a
    // focus-restore that ran afterwards would see focus on the body and could
    // no longer tell "the user was typing in the shell" from "the user closed
    // it from the transcript".
    deps.focusComposer?.(ownerId)
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
      setEntries(produce((state) => {
        delete state[ownerId]
      }))
      return
    }
    // Reopened while it was sliding out. The panel stays, and it gets the fresh
    // shell the user is now looking at an empty pane waiting for.
    setEntries(ownerId, 'terminalId', '')
    const owner = deps.getAgentTab(ownerId)
    if (!owner) {
      setEntries(produce((state) => {
        delete state[ownerId]
      }))
      return
    }
    void resolveTerminal(owner).catch((err) => {
      release(ownerId)
      showWarnToast('Failed to open the quake terminal', err)
    })
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
    // Before the retract, for the reason `close` states.
    deps.focusComposer?.(ownerId)
    setEntries(ownerId, 'open', false)
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
