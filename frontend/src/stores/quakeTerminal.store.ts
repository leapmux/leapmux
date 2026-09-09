import type { DetachedTerminal } from './tabView'
import type { Tab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import { createStore, produce } from 'solid-js/store'
import * as workerRpc from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { disposeTerminalInstance } from '~/components/terminal/TerminalView'
import { DEFAULT_TERMINAL_COLS, DEFAULT_TERMINAL_ROWS } from '~/lib/terminal'
import { openedTerminalMetadata, terminalMetadata } from '~/stores/tab.helpers'

/**
 * What a quake terminal BELONGS TO: one working directory on one worker.
 *
 * Not a tab. Every tab that works in that directory -- agent, terminal, file
 * viewer or image viewer alike -- reaches the same shell, which is the whole
 * point: two agent tabs on one checkout share a build, a `git status` and a
 * scrollback instead of each getting a private PTY the other cannot see.
 *
 * The WORKER is half the key and not decoration: two workers can both have
 * `/home/me/proj`, and they are two different directories on two different
 * machines.
 */
export interface QuakeKey {
  workerId: string
  workingDir: string
}

/**
 * The key as ONE string, for the store's record and for every comparison.
 *
 * A NUL separates the halves because it can occur in neither: a path cannot
 * contain one on any platform this runs on, and a worker id is a nanoid. A `:`
 * would have been ambiguous with a Windows drive letter, and a space occurs in
 * plenty of real paths.
 *
 * Written as an ESCAPE, never as a literal: a raw NUL byte in a source file is
 * invisible in every editor and survives a copy-paste as whitespace.
 */
export function quakeKeyId(key: QuakeKey): string {
  return `${key.workerId}\u0000${key.workingDir}`
}

/**
 * The key of a tab, or undefined when that tab names no quake terminal.
 *
 * A tab with no worker (not hydrated yet) or no working directory has no
 * directory to put a shell in. Every OTHER tab type does: this is deliberately
 * not restricted to agent tabs, so the panel is available over a terminal tab,
 * a file viewer and an image viewer too -- see the note on `open`.
 */
export function quakeKeyForTab(tab: Tab | undefined): QuakeKey | undefined {
  if (!tab?.workerId || !tab.workingDir)
    return undefined
  return { workerId: tab.workerId, workingDir: tab.workingDir }
}

/**
 * One working directory's quake terminal: the shell that slides over the
 * centre area.
 */
export interface QuakeEntry {
  /** `quakeKeyId` of the directory this shell belongs to. The store's key. */
  keyId: string
  workerId: string
  workingDir: string
  /**
   * The workspace a cold open was refused or allowed in, from the tab that
   * opened the panel.
   *
   * STORED rather than derived, and refreshed on every `open`. Deriving it
   * through `deps.tabForKey` would make `detachedTerminals` read the tab view,
   * which is the exact back-edge `AppShell` builds this store BEFORE the view
   * to avoid. A directory whose last tab is gone is retired by
   * `retireStaleKeys` a moment later, so the stale value has no reader.
   */
  workspaceId: string
  /**
   * The quake terminal, once the worker answered.
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

/** A resolved entry: one whose quake terminal the worker already gave. */
export type ResolvedQuakeEntry = QuakeEntry & { terminalId: string }

/** What the store needs from the shell to resolve and record a quake terminal. */
export interface QuakeTerminalDeps {
  metadata: TabMetadataStore
  /**
   * A live tab that works in one quake key's directory, or undefined when none
   * does any more.
   *
   * Read at call time rather than captured, because the CLI command path gives
   * a directory this store never saw and must resolve it against the live tab
   * set. It answers three questions with one lookup: which workspace a cold
   * open is refused in, which tab row a background shell's badge belongs on,
   * and whether the key still has any reason to exist.
   */
  tabForKey: (key: QuakeKey) => Tab | undefined
  /**
   * Restore keyboard focus to the composer when a panel closes.
   *
   * `terminalId` is the shell THIS close retracts, and it is the whole point of
   * the argument. One panel element holds every directory's terminal, so
   * "is focus inside the panel?" is true whenever ANY of them has the caret
   * -- and a background directory's close (its shell exited, or another device
   * ran the Control CLI) would then pull the caret out of the foreground shell
   * the user types in. The implementation compares this id against the focused
   * terminal instead.
   *
   * Called while the panel is still open, so focus is still where the user left
   * it: closing the panel marks it `inert`, which blurs whatever it holds.
   *
   * `terminalId` is absent when the RPC has not resolved yet, which is also
   * when no terminal of this entry can hold focus.
   */
  focusComposer?: (keyId: string, terminalId: string | undefined) => void
  /** How long the panel takes to retract, so a dispose can wait it out. */
  closeDelayMs: () => number
  /**
   * Whether ONE workspace, by id, can be mutated -- false while it is archived,
   * and false for one that no longer exists.
   *
   * Consulted on the OPENING direction alone, and it lives here rather than in
   * each caller so the keyboard commands and the Control CLI cannot answer
   * differently. Per id, because a CLI request gives a directory whose tabs are
   * in a workspace this client does not display.
   */
  isWorkspaceMutatable: (workspaceId: string) => boolean
}

/**
 * The quake terminals this client knows about, and which panels are visible.
 *
 * NEVER stamp MRU on a quake terminal id. The MRU map is persisted, and
 * `useMetadataSweep` seeds its `seen` set from the persisted rows on the next
 * page load -- so one stamp makes the sweep retire a LIVE terminal's metadata
 * row mid-session, taking its screen and cursor with it. A quake terminal is
 * not a tab and is never selected, so nothing should be tempted to; this note
 * is here because the failure is silent and a page load away from its cause.
 *
 * The open state is per-client on purpose. The SHELL is shared -- one PTY per
 * (worker, directory), which every device and every tab in that directory
 * attaches to -- but whether the panel is visible is the same kind of fact as
 * which tab is active in a tile, which this codebase deliberately keeps
 * client-local.
 */
export function createQuakeTerminalStore(deps: QuakeTerminalDeps) {
  const [entries, setEntries] = createStore<Record<string, QuakeEntry>>({})

  const entryFor = (keyId: string): QuakeEntry | undefined => entries[keyId]

  /**
   * Every panel this client holds whose shell is resolved.
   *
   * The watch plan reads this rather than mapping ids back through `keyOf`:
   * it needs the key and the open state together, and both are right here.
   */
  const liveEntries = (): ResolvedQuakeEntry[] =>
    Object.values(entries).filter((entry): entry is ResolvedQuakeEntry => entry.terminalId !== undefined)

  /** Every quake terminal this client holds, for the tab view's detached family. */
  const detachedTerminals = (): DetachedTerminal[] => {
    const out: DetachedTerminal[] = []
    for (const entry of liveEntries())
      out.push({ id: entry.terminalId, workerId: entry.workerId, workspaceId: entry.workspaceId })
    return out
  }

  const keyOf = (terminalId: string): string | undefined =>
    Object.values(entries).find(e => e.terminalId === terminalId)?.keyId

  const isQuakeTerminal = (terminalId: string): boolean => keyOf(terminalId) !== undefined

  /**
   * The tab a background shell's notification badge belongs on.
   *
   * A quake terminal has no row any surface renders, so its own id is the wrong
   * badge target -- see `detachedOwnerOf` in `~/hooks/terminalEvents`. Any tab
   * in its directory IS rendered, and it is where the user goes to reach the
   * panel.
   */
  const badgeTabFor = (terminalId: string): string | undefined => {
    const keyId = keyOf(terminalId)
    if (keyId === undefined)
      return undefined
    const entry = entries[keyId]
    return entry === undefined ? undefined : deps.tabForKey(entry)?.id
  }

  /** Let go of one dead terminal's xterm instance and metadata row. */
  const releaseTerminal = (terminalId: string) => {
    disposeTerminalInstance(terminalId, { captureScreen: false })
    deps.metadata.remove(terminalId)
  }

  /** Forget one key's entry. The store's only delete. */
  const dropEntry = (keyId: string) => {
    setEntries(produce((state) => {
      delete state[keyId]
    }))
  }

  /** Drop an entry and release everything it holds. Safe to run twice. */
  const release = (keyId: string) => {
    const entry = entries[keyId]
    if (!entry)
      return
    // `captureScreen: false` for the reason `handleTerminalClose` gives: the
    // terminal ends here, so a serialized buffer has no future reader.
    if (entry.terminalId !== undefined)
      releaseTerminal(entry.terminalId)
    dropEntry(keyId)
  }

  /**
   * Abandon a panel whose open failed, and say so.
   *
   * No half-entry survives a failure: the next toggle must start clean rather
   * than show an empty panel for ever.
   */
  const failOpen = (keyId: string, err: unknown) => {
    release(keyId)
    showWarnToast('Failed to open the quake terminal', err)
  }

  /**
   * Resolve the quake terminal for a key: adopt the one the worker already has,
   * or ask it to spawn one.
   *
   * The list MUST precede the open. Inverting them would ask for a second shell
   * the worker's unique index then refuses, and -- worse -- a client that read
   * its own refusal as a failure would show an error for a panel that is
   * working. The list is also what makes a SECOND DEVICE, and a second TAB in
   * the same directory, attach to the first shell rather than start its own.
   */
  const resolveTerminal = async (key: QuakeKey): Promise<void> => {
    const keyId = quakeKeyId(key)
    const existing = await workerRpc.listTerminals(key.workerId, { tabIds: [], quakeWorkingDirs: [key.workingDir] })
    const adopted = existing.terminals.find(t => t.quake)
    if (adopted) {
      recordResolvedTerminal(keyId, adopted.terminalId, () =>
        deps.metadata.patch(adopted.terminalId, terminalMetadata(key.workerId, adopted)))
      return
    }
    const resp = await workerRpc.openTerminal(key.workerId, {
      cols: DEFAULT_TERMINAL_COLS,
      rows: DEFAULT_TERMINAL_ROWS,
      workingDir: key.workingDir,
      shell: '',
      workerId: key.workerId,
      shellStartDir: '',
      quake: true,
    })
    recordResolvedTerminal(keyId, resp.terminalId, () =>
      deps.metadata.patch(resp.terminalId, openedTerminalMetadata({
        title: resp.title,
        workingDir: key.workingDir,
      })))
  }

  /**
   * Attach a resolved terminal to its key's entry, unless the entry is gone.
   *
   * The entry CAN be gone, because `resolveTerminal` awaits two RPCs and the
   * last tab in the directory can close in that window -- `retireStaleKeys`
   * then deletes the key. A `setEntries(keyId, 'terminalId', ...)` on a deleted
   * key does not no-op: Solid's `updatePath` dereferences the absent parent and
   * THROWS, the throw surfaces as a rejected promise, and the caller's catch
   * shows "Failed to open the quake terminal" for a tab the user merely closed.
   *
   * The shell the worker may have spawned in that window is left to the WORKER,
   * for the reason `retireStaleKeys` gives: the close of the last tab in a
   * directory closes its quake terminal on every close path, and the orphan
   * reconciler reaps one whose directory has no tabs left. A CloseTerminal from
   * here would be a second teardown racing those.
   */
  function recordResolvedTerminal(keyId: string, terminalId: string, applyMetadata: () => void) {
    if (entries[keyId] === undefined)
      return
    applyMetadata()
    setEntries(keyId, 'terminalId', terminalId)
  }

  /**
   * Show the panel of the directory `tab` works in.
   *
   * Takes a TAB rather than a key, and any tab type will do. The panel is
   * available over a terminal tab, a file viewer and an image viewer exactly as
   * it is over an agent tab: what it needs is a worker and a directory, and
   * every tab type carries both. A tab missing either simply has no panel.
   */
  const open = async (tab: Tab): Promise<void> => {
    const key = quakeKeyForTab(tab)
    if (!key)
      return
    const keyId = quakeKeyId(key)
    const existing = entries[keyId]
    if (existing) {
      // Already resolved, or resolving. Either way the panel just shows: no
      // RPC, which is what makes a toggle free and what keeps the shell alive
      // across one.
      //
      // The workspace is re-stamped because the tab that reaches a shared shell
      // this time can be in a different workspace from the one that opened it.
      setEntries(keyId, { workspaceId: tab.workspaceId, open: true })
      return
    }
    // Refused HERE, not at each call site, and only for a cold open: starting a
    // shell is a mutation of an archived workspace, and hiding a panel is not.
    // A guard on the whole toggle stranded a user whose workspace was archived
    // while the panel was up -- it covers the entire centre area and carries no
    // close control of its own.
    if (!deps.isWorkspaceMutatable(tab.workspaceId))
      return
    // The entry lands BEFORE the RPC, so the panel slides in while the worker
    // is still answering and shows the shell's own startup state. A panel that
    // appeared only after the round trip would jump into place on a cold open.
    // The panel itself owns the first frame -- see `armFirstSlide` in
    // `~/components/shell/QuakeTerminalPanel`.
    setEntries(keyId, {
      keyId,
      workerId: key.workerId,
      workingDir: key.workingDir,
      workspaceId: tab.workspaceId,
      open: true,
    })
    try {
      await resolveTerminal(key)
    }
    catch (err) {
      failOpen(keyId, err)
    }
  }

  const close = (keyId: string) => {
    if (!entries[keyId]?.open)
      return
    // Asked BEFORE the panel retracts, and the order is load-bearing. Closing
    // the panel marks it `inert`, which blurs whatever it holds -- so a
    // focus-restore that ran afterwards would see focus on the body and could
    // no longer tell "the user typed in the shell" from "the user closed
    // it from the transcript".
    deps.focusComposer?.(keyId, entries[keyId]?.terminalId)
    setEntries(keyId, 'open', false)
  }

  const toggle = (tab: Tab): void => {
    const key = quakeKeyForTab(tab)
    if (!key)
      return
    const keyId = quakeKeyId(key)
    if (entries[keyId]?.open)
      close(keyId)
    else
      void open(tab)
  }

  /**
   * Finish what `handleShellExit` started, once the panel has slid away.
   *
   * The user can toggle the panel back on DURING the retract, and that decides
   * which of two things happens here. It is a real window -- the animation is
   * 300 ms by default -- and getting it wrong strands the user in front of a
   * panel that unmounts itself a moment after they asked for it.
   */
  const finishShellExit = (keyId: string, deadTerminalId: string) => {
    const entry = entries[keyId]
    releaseTerminal(deadTerminalId)
    if (!entry || entry.terminalId !== deadTerminalId) {
      // Something already moved this key on -- its last tab closed, or a later
      // exit overtook this timer. The dispose above is all that was left.
      return
    }
    if (!entry.open) {
      dropEntry(keyId)
      return
    }
    // Reopened during the retract. The panel stays, and it gets the fresh
    // shell that the user now waits for in front of an empty pane.
    setEntries(keyId, 'terminalId', undefined)
    const tab = deps.tabForKey(entry)
    // A fresh shell is a COLD open, so it takes the same refusal `open` applies
    // -- the workspace can have been archived while the panel was up, and the
    // reopen-during-retract window is precisely where that guard was missing.
    // Without it the client asks the worker for a shell in an archived
    // workspace and shows a failure toast for its own request.
    if (!tab || !deps.isWorkspaceMutatable(tab.workspaceId)) {
      dropEntry(keyId)
      return
    }
    void resolveTerminal(entry).catch(err => failOpen(keyId, err))
  }

  /**
   * The shell exited (the user typed `exit`, or it died).
   *
   * The panel RETRACTS with its usual slide before the instance is disposed, so
   * the user watches it leave instead of the content blanking underneath them.
   *
   * No CloseTerminal RPC: the worker closes a quake terminal's row itself when
   * its shell exits, because it has no restart contract. The next open
   * therefore misses the directory lookup and spawns a fresh shell --
   * deliberately NOT the "press Enter to restart" a terminal TAB offers, since a
   * quake terminal the user ended should not leave a dead pane in front of them.
   */
  const handleShellExit = (terminalId: string) => {
    const keyId = keyOf(terminalId)
    if (keyId === undefined)
      return
    // The same retract the user's own close performs, including the focus
    // restore it asks for first. `keyOf` matched this entry on `terminalId`,
    // so `close` reports the identical shell.
    close(keyId)
    const wait = deps.closeDelayMs()
    if (wait <= 0) {
      finishShellExit(keyId, terminalId)
      return
    }
    setTimeout(finishShellExit, wait, keyId, terminalId)
  }

  /**
   * Let go of every panel whose directory has no tab left.
   *
   * Driven by the metadata sweep's "was live, now is not" edge, but it asks the
   * question about the DIRECTORY rather than about the retired ids: the last
   * tab of a directory closing is what ends a quake terminal, and which tabs
   * those were is not something this store tracks. Re-checking every entry is
   * cheap -- there is one per directory the user opened a panel in.
   *
   * Emits NO CRDT op: a quake terminal has no record, and `applyTombstoneTab`
   * MATERIALIZES a record for an id it does not know -- so a tombstone here
   * would inject a phantom tab into the account. Issues no CloseTerminal
   * either: the worker's own close path already closed the shell
   * authoritatively, from whichever device ran it, and a second call from here
   * would race that teardown.
   */
  const retireStaleKeys = () => {
    // The keys are collected BEFORE the first release: `release` deletes from
    // the store the loop would otherwise still be walking, and the entries it
    // walks are store proxies whose node is gone once deleted.
    const stale = Object.values(entries)
      .filter(entry => deps.tabForKey(entry) === undefined)
      .map(entry => entry.keyId)
    for (const keyId of stale)
      release(keyId)
  }

  return {
    entryFor,
    liveEntries,
    detachedTerminals,
    keyOf,
    badgeTabFor,
    isQuakeTerminal,
    open,
    close,
    toggle,
    handleShellExit,
    retireStaleKeys,
  }
}

export type QuakeTerminalStore = ReturnType<typeof createQuakeTerminalStore>
