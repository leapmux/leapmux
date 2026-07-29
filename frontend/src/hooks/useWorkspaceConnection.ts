import type { AgentEvent, TerminalEvent, WatchAgentEntry } from '~/generated/leapmux/v1/workspace_pb'
import type { createLoadingSignal } from '~/hooks/createLoadingSignal'
import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { AgentTab, Tab } from '~/stores/tab.types'
import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'

import { batch, createEffect, createSignal, onCleanup, untrack } from 'solid-js'
import { watchEventsViaChannel } from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { getTerminalInstance } from '~/components/terminal/TerminalView'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { waitForStreamCompletion } from '~/hooks/streamCompletion'
import { ChannelError } from '~/lib/channel'
import { createLogger } from '~/lib/logger'
import { createExponentialBackoff } from '~/lib/retry'
import { applyTerminalData, bufferHasVisibleContent } from '~/lib/terminal'
import { parseTabKey } from '~/stores/tab.helpers'
import {
  applyBackgroundAgentStatusChange,
  handleAgentMessage,
  handleAgentStatusChange,
  handleControlRequest,
  handleStreamChunk,
  handleStreamEnd,
} from './agentEvents'
import { applyTerminalStatusChange, markTerminalExited } from './terminalEvents'
import { agentWatchEntry, buildWatchTargetsKey, unsubscribeAllWatchEvents } from './watchTargets'

const log = createLogger('workspace')

/**
 * Which tabs a worker going offline affects.
 *
 * Both arms filter on `workerId`, and that is the whole point: this walks
 * `view.all()` — every tab in the ACCOUNT, not one workspace — so a tab hosted
 * by any other worker is still perfectly connected. Clearing an agent's
 * `streamingText` discards deltas that will never be resent, and flipping it to
 * INACTIVE hides a thinking indicator for a turn that is still running. The
 * agent arm was missing the filter, which the account-wide widening turned from
 * a one-workspace bug into an account-wide one.
 *
 * Only READY terminals are affected: one already DISCONNECTED or EXITED has
 * nothing to lose, and re-patching it would churn the join for no reason.
 *
 * Pure and exported so the filter is testable without mounting the connection
 * hook, whose sweep is a reactive effect.
 */
export function collectWorkerOfflineTargets(
  tabs: readonly Tab[],
  workerId: string,
): { terminals: Set<string>, agents: AgentTab[] } {
  const terminals = new Set<string>()
  const agents: AgentTab[] = []
  for (const tab of tabs) {
    if (tab.workerId !== workerId)
      continue
    if (tab.type === TabType.TERMINAL && tab.status === TerminalStatus.READY)
      terminals.add(tab.id)
    else if (tab.type === TabType.AGENT)
      agents.push(tab)
  }
  return { terminals, agents }
}

/**
 * Forward-fill the window->live tail gap for every agent tab that lags its recorded
 * live tail. Two cases, both run inside a reactive effect (see useWorkspaceConnection) so
 * this re-evaluates whenever any agent's window tail / recorded live tail / deferral flag
 * moves -- replacing the one-shot forward-fill that fired only on CatchUpComplete:
 *  - reader AT the tail (hasNewerMessages false but NOT caught up): catchUpToTail drains
 *    the gap via its addMessage path.
 *  - an EXHAUSTION-FORCED park (hasNewerMessages true because a broadcast storm outran the
 *    bounded forward-fill, NOT a settled scrolled-away wall): resumeDeferredTailFill
 *    resumes the bounded fill so a FOLLOWING reader self-heals as the storm drains. A
 *    plain scrolled-away hasNewerMessages (deferral flag clear) is left to the affordance.
 * Reads ALL invariant terms per agent so the effect subscribes to each. Both fillers are
 * idempotent (no-op while one is already draining the agent), so a re-run on their own
 * per-page writes does no work; a tab with an empty workerId (a non-active-workspace
 * agent) is skipped.
 */
export function reconcileLaggingTails(deps: {
  agentTabs: () => ReadonlyArray<{ id: string, workerId: string }>
  hasNewerMessages: (agentId: string) => boolean
  caughtUpToLiveTail: (agentId: string) => boolean
  isTailFillDeferred: (agentId: string) => boolean
  getLastSeq: (agentId: string) => bigint
  isFetchingNewer: (agentId: string) => boolean
  catchUpToTail: (workerId: string, agentId: string, afterSeq: bigint) => void
  resumeDeferredTailFill: (workerId: string, agentId: string) => void
  jumpToLatest: (workerId: string, agentId: string) => void
}): void {
  for (const tab of deps.agentTabs()) {
    if (!tab.workerId)
      continue
    const atTail = !deps.hasNewerMessages(tab.id)
    const caughtUp = deps.caughtUpToLiveTail(tab.id)
    const deferred = deps.isTailFillDeferred(tab.id)
    if (caughtUp)
      continue
    // The window is EMPTY (getLastSeq 0n) while server content still exists (liveTail > 0,
    // so not caughtUp): e.g. a full phantom reap on reconnect dropped every loaded row
    // (all were tail rows deleted while disconnected) but older history survives. There's
    // no loaded anchor to forward-fill from, so re-seat on the latest page. Guarded on the
    // in-flight flag so the re-seat isn't re-issued each reconcile tick while it resolves
    // (jumpToLatest aborts + restarts its own fetch, which would otherwise loop).
    if (deps.getLastSeq(tab.id) === 0n) {
      if (!deps.isFetchingNewer(tab.id))
        deps.jumpToLatest(tab.workerId, tab.id)
      continue
    }
    if (atTail)
      deps.catchUpToTail(tab.workerId, tab.id, deps.getLastSeq(tab.id))
    else if (deferred)
      deps.resumeDeferredTailFill(tab.workerId, tab.id)
  }
}

export interface WorkspaceConnectionParams {
  chatStore: ReturnType<typeof createChatStore>
  view: TabView
  metadata: TabMetadataStore
  selection: TabSelectionStore
  controlStore: ReturnType<typeof createControlStore>
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
  settingsLoading: ReturnType<typeof createLoadingSignal>
  getActiveWorkspaceId: () => string | null
  /** Returns the worker ID for the active workspace. */
  getWorkerId: () => string
  /** Called when an agent turn ends (turn completed or control request received). */
  onTurnEnd?: (agentId: string, numToolUses?: number) => void
}

export function useWorkspaceConnection(params: WorkspaceConnectionParams) {
  const { chatStore, view, metadata, selection, controlStore, agentSessionStore, settingsLoading } = params
  const [workerOnline, setWorkerOnline] = createSignal(true)

  // Single unified event stream abort controller.
  let eventStreamAbort: AbortController | null = null
  // Serialized key of the current subscription set to detect changes.
  let currentTargetsKey = ''

  // Agent/terminal ids belonging to a workspace other than the active one.
  // Events for these get lightweight handling — status and git fields only —
  // rather than full chat processing.
  //
  // Still needed by `buildWatchTargetsKey`: the same id moving between active
  // and non-active handling must restart the stream, so the role is part of the
  // subscription key. They are derived in the watch effect from the view; the
  // parallel per-workspace record they used to mirror no longer exists.

  /**
   * Is this tab in the workspace the user is looking at?
   *
   * The event handlers branch on this to decide whether to do full chat /
   * control processing or just patch status metadata. It is a live read off the
   * joined view — the tab carries its own workspace — where before it was a
   * lookup against sets rebuilt from registry snapshots.
   */
  function isInActiveWorkspace(type: TabType, tabId: string): boolean {
    const tab = params.view.getById(type, tabId)
    return !!tab && tab.workspaceId === params.getActiveWorkspaceId()
  }

  // Handle an agent event from the unified stream.
  const handleAgentEvent = (
    agentEvent: AgentEvent,
    catchUpPhases: Map<string, 'catchingUp' | 'live'>,
    // The resume cursor (the client's loaded/recorded tail) this subscribe sent per
    // agent, captured at subscribe time. Used as the CatchUpStart reap ceiling so a
    // live message that raced in AFTER subscribe (seq above it) isn't reaped as a
    // phantom -- see the catchUpStart case.
    resumeTails: Map<string, bigint>,
  ) => {
    const agentId = agentEvent.agentId
    const inner = agentEvent.event

    // Agent in a workspace the user isn't looking at: take status and git, skip
    // everything else.
    //
    // This drop is DELIBERATE and survives the move to flat state -- it is a
    // memory bound, not an artefact of the old active/inactive store split.
    // Live controlRequest / controlCancel / agentMessage / streamChunk events
    // for these agents are dropped because the user cannot see them, and the
    // WatchEvents catch-up replay on workspace switch re-reads pending
    // control_requests from the DB, so any still-pending prompt is re-delivered
    // through the full handler at that point. The DB row is the source of
    // truth; live broadcasts are an optimisation for what's on screen.
    if (!isInActiveWorkspace(TabType.AGENT, agentId)) {
      // Status goes through the SAME transition the foreground path uses. One
      // patch, keyed by tab id: which workspace owns the agent no longer has to
      // be discovered -- metadata is flat, and the sidebar reads the same row
      // whatever workspace the user is looking at. Re-stating a subset here is
      // how the pending-message drain went missing; see
      // `applyBackgroundAgentStatusChange`.
      if (inner.case === 'statusChange')
        applyBackgroundAgentStatusChange(inner.value, params, params.settingsLoading)
      return
    }

    // Get or initialize catch-up phase for this agent.
    const catchUpPhase = catchUpPhases.get(agentId) ?? 'live'
    const markLiveAgentActive = () => {
      if (catchUpPhase !== 'live')
        return
      setWorkerOnline(true)
      const current = view.getAgentTab(agentId)
      if (current?.agentStatus === AgentStatus.INACTIVE) {
        metadata.patch(agentId, { agentStatus: AgentStatus.ACTIVE })
      }
    }

    switch (inner.case) {
      case 'agentMessage':
        markLiveAgentActive()
        handleAgentMessage(
          agentId,
          inner.value,
          { agentSessionStore, chatStore, view, metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId },
          params.onTurnEnd,
          catchUpPhase,
        )
        break
      case 'streamChunk':
        markLiveAgentActive()
        handleStreamChunk(agentId, inner.value, chatStore)
        break
      case 'streamEnd':
        markLiveAgentActive()
        handleStreamEnd(agentId, inner.value, { chatStore, view, metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId })
        break
      case 'statusChange':
        handleAgentStatusChange(
          agentId,
          inner.value,
          catchUpPhase,
          { agentSessionStore, chatStore, view, metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId, controlStore },
          settingsLoading,
          setWorkerOnline,
          params.onTurnEnd,
        )
        break
      case 'controlRequest':
        markLiveAgentActive()
        handleControlRequest(
          agentId,
          inner.value,
          catchUpPhase,
          { agentSessionStore, chatStore, view, metadata, selection, getActiveWorkspaceId: params.getActiveWorkspaceId, controlStore },
          params.onTurnEnd,
        )
        break
      case 'controlCancel': {
        const cc = inner.value
        controlStore.removeRequest(cc.agentId, cc.requestId)
        break
      }
      case 'messageError': {
        const me = inner.value
        if (me.error) {
          chatStore.setMessageError(me.messageId, me.error)
        }
        else {
          chatStore.clearMessageError(me.messageId)
        }
        break
      }
      case 'messageDeleted': {
        const md = inner.value
        // Pass the deleted row's seq (so removeMessage can tell whether it was the
        // recorded live tail) and the authoritative post-delete tail (so it can set
        // the high-water to exactly that, even when the deleted row was unloaded
        // beyond the window) -- no deletedSeq-1 guesswork.
        chatStore.removeMessage(md.agentId, md.messageId, md.seq, md.newLatestSeq)
        break
      }
      case 'todosChanged': {
        // Sole driver of the sidebar to-do list. The worker persists
        // every to-do event in agent_todos and ships the post-mutation
        // snapshot here; clients replace wholesale.
        const tc = inner.value
        chatStore.todos.replace(tc.agentId, tc.todos)
        break
      }
      case 'catchUpStart':
        // Pre-trim BEFORE the message replay renders: the worker ships the
        // authoritative live tail up front so a reconnecting client drops phantom rows
        // (a tail it loaded before disconnect that was deleted while away) immediately,
        // rather than flashing them until catchUpComplete reconciles at the end. An
        // unset latest_seq (worker couldn't determine the tail) is skipped by the store.
        //
        // At catch-up START, latest_seq IS the start tail, so a phantom and a live
        // message that raced in are indistinguishable by seq alone -- both sit above
        // latest_seq. The watcher is registered BEFORE the worker reads the tail, so
        // such a live frame CAN land before this one (a tight race). Bound the reap by
        // the resume cursor this subscribe sent (the client's loaded tail): a row above
        // it arrived AFTER subscribe -- a genuine live arrival -- and is exempted, while
        // a phantom (a once-loaded row, so seq <= the resume cursor) is still reaped.
        // catchUpComplete then reconciles again with the authoritative band.
        chatStore.reconcileAuthoritativeTail(agentId, inner.value.latestSeq, resumeTails.get(agentId))
        break
      case 'catchUpComplete':
        catchUpPhases.set(agentId, 'live')
        // Catch-up done: the live-append guard returns to the recorded-live-tail
        // comparison (which correctly splices a post-delete message). Clear BEFORE the
        // reconcile so the probe's contiguous forward-fill pages aren't re-dropped.
        chatStore.setCatchingUp(agentId, false)
        // Reconcile the window to the authoritative live tail the worker reports (the
        // final authority after the replay burst; catchUpStart did an early pass). A
        // reconnecting client never received the AgentMessageDeleted for rows deleted
        // while it was disconnected, so it drops phantom rows beyond latest_seq and
        // clamps its recorded live-tail -- otherwise its "new messages below"
        // affordance can stay stuck past a now-shorter history. An unset latest_seq
        // (worker couldn't determine the tail) is skipped for the reap, but PROBED (see
        // probeIndeterminate below). start_tail_seq (the tail when replay began) bounds
        // the reap so a live message that raced in DURING catch-up -- seq above it --
        // isn't reaped as a phantom. When start_tail_seq is indeterminate (unset, a failed
        // worker readback), fall back to the resume cursor (the loaded tail this subscribe
        // sent) as the ceiling -- the SAME bound catchUpStart uses -- so a live arrival
        // above it is still exempted instead of reaping everything beyond latest_seq and
        // losing the raced-in message.
        //
        // The bounded replay (<= 50 rows) may not have reached the tail, but we do NOT
        // forward-fill here: reconcileAuthoritativeTail raises the recorded live tail to
        // latest_seq, and the continuous tail-reconcile effect (below) forward-fills
        // whenever the loaded tail lags it -- so a windowed-away reader keeps their
        // position + affordance, and a following reader's gap closes without hinging on
        // this frame. The ONE exception folded into the reconcile is an INDETERMINATE
        // (unset) tail: liveTail can't be raised, so probeIndeterminate=true nudges it one
        // past the loaded tail to make the continuous reconcile probe (it would otherwise
        // read the partial replay as caught up). This keeps ALL forward-fill in the
        // continuous reconcile -- there is no one-shot fill in this handler.
        chatStore.reconcileAuthoritativeTail(
          agentId,
          inner.value.latestSeq,
          inner.value.startTailSeq === undefined
            ? resumeTails.get(agentId)
            : inner.value.startTailSeq,
          true,
        )
        // Re-seed the scroll-rail marks so any user sends / deletes that happened while
        // disconnected are reflected (live add/remove already heals the connected case). Tied
        // to this subscription's signal so a resubscribe/teardown cancels it (see loadMessageMarks).
        void chatStore.loadMessageMarks(view.getAgentTab(agentId)?.workerId ?? '', agentId, eventStreamAbort?.signal)
        // Reclaim any command stream orphaned DURING catch-up: a mid-stream
        // delete (or beyond-window reseq) recorded an orphan, but its turn-end
        // divider replayed while the phase was still 'catchingUp', so the
        // turn-end sweep above was skipped. Drain once here on the transition so
        // an orphan can't sit stuck until the next live turn-end (or forever, if
        // none follows). No-op when nothing was orphaned.
        chatStore.sweepOrphanedBufferedSpans(agentId)
        break
    }
  }

  // Handle a terminal event from the unified stream.
  const handleTerminalEvent = (termEvent: TerminalEvent) => {
    const terminalId = termEvent.terminalId

    // Terminal in a workspace other than the active one — skip data events (no
    // xterm instance exists to write them to), but handle closed + statusChange
    // so the sidebar badge's status / gitBranch / gitOriginUrl stay fresh.
    // Same rule as agents: a terminal the user isn't looking at gets status and
    // git only. `data` is skipped because no xterm instance exists to write it
    // to -- the screen is re-fetched on switch-in.
    //
    // Both non-`data` arms route through the SAME transitions the active arm
    // uses. Re-stating a subset here is what let the status half go missing:
    // the sidebar renders every workspace, so a status it cannot see is a
    // spinner that never stops.
    if (!isInActiveWorkspace(TabType.TERMINAL, terminalId)) {
      if (termEvent.event.case === 'closed') {
        // A tab the user switches INTO must not still be showing a dead
        // startup spinner.
        markTerminalExited(params.metadata, terminalId)
      }
      else if (termEvent.event.case === 'statusChange') {
        applyTerminalStatusChange(
          params.metadata,
          view.getTerminalTab(terminalId),
          terminalId,
          termEvent.event.value,
        )
      }
      return
    }

    switch (termEvent.event.case) {
      case 'data': {
        const instance = getTerminalInstance(terminalId)
        if (instance) {
          const tab = view.getTerminalTab(terminalId)
          const checkContent = tab && !tab.contentReady
          const onParsed = () => {
            if (checkContent && bufferHasVisibleContent(instance.terminal))
              metadata.patch(terminalId, { contentReady: true })
          }
          const { data, isSnapshot, endOffset } = termEvent.event.value
          const newOffset = applyTerminalData(
            instance,
            data,
            isSnapshot,
            Number(endOffset),
            // Straight from the store: this is written per PTY read, and the
            // join no longer carries it (see `TerminalMeta.lastOffset`).
            metadata.get(terminalId)?.lastOffset ?? 0,
            onParsed,
          )
          metadata.patch(terminalId, { lastOffset: newOffset })
        }
        break
      }
      case 'closed':
        markTerminalExited(metadata, terminalId)
        break
      case 'statusChange': {
        const sc = termEvent.event.value
        // Only propagate into the tab store when the server reports a
        // terminal lifecycle transition — STARTING, READY, or
        // STARTUP_FAILED. READY and STARTUP_FAILED both arrive on
        // normal subscribe via WatchEvents's catch-up, so the race of a
        // late subscriber missing the one-shot broadcast is closed.
        applyTerminalStatusChange(metadata, view.getTerminalTab(terminalId), terminalId, sc)
        break
      }
    }
  }

  // Previous stream handle, kept alive during the gap between abort and
  // new stream registration so the server-side watcher can still deliver
  // terminal data until the new WatchEvents updates its routing.
  let previousHandle: { close: () => void } | null = null

  // Unified event stream via E2EE channel with retry.
  const watchEvents = async (
    agentEntries: WatchAgentEntry[],
    terminalIds: string[],
    // The partition this run was STARTED with, passed rather than read from
    // hook scope. The body awaits, so reading shared mutable state after an
    // await would sample a partition a later effect run may have rewritten.
    // Today that cannot bite -- a membership change rewrites the subscription
    // key, which aborts this run in the same synchronous pass -- but passing it
    // makes the hazard unrepresentable instead of merely absent.
    nonActiveAgentIds: ReadonlySet<string>,
    signal: AbortSignal,
  ) => {
    // Load initial messages for active workspace agents only. Non-active
    // workspace agents only receive lightweight status/git updates — they
    // don't need full chat history loaded.
    await Promise.all(
      agentEntries
        .filter(entry => !nonActiveAgentIds.has(entry.agentId))
        .map(async (entry) => {
          try {
            const wid = view.getAgentTab(entry.agentId)?.workerId ?? ''
            await chatStore.loadInitialMessages(wid, entry.agentId)
            // Seed the scroll-rail marks alongside history (not awaited: a failure
            // must not block or fail the history load -- the rail just stays hidden). Tied to
            // this subscription's signal so a resubscribe/teardown cancels it.
            void chatStore.loadMessageMarks(wid, entry.agentId, eventStreamAbort?.signal)
          }
          catch (err) {
            showWarnToast('Failed to load chat history', err)
          }
        }),
    )

    if (signal.aborted)
      return

    // Per-agent catch-up phase tracking.
    const catchUpPhases = new Map<string, 'catchingUp' | 'live'>()
    for (const entry of agentEntries) {
      catchUpPhases.set(entry.agentId, 'catchingUp')
    }
    // The resume cursor sent per agent on the current (re)subscribe, captured below so
    // CatchUpStart can exempt live arrivals that post-date it from the phantom reap.
    const resumeTails = new Map<string, bigint>()

    // Per-loop reconnect backoff. Successful events reset the
    // sequence; stream-level errors also reset (the legacy code did
    // `Math.min(backoff, 500)` to retry fast, which the helper's
    // initial-delay floor of 1s approximates closely enough). Only
    // sustained connection-lost errors let the sequence walk up to
    // 30s.
    const backoff = createExponentialBackoff<string>({
      initialMs: 1000,
      maxMs: 30000,
      multiplier: 2,
      jitterFactor: 0,
    })
    const BACKOFF_KEY = 'watch'
    signal.addEventListener('abort', () => backoff.cancelAll(), { once: true })

    while (!signal.aborted) {
      try {
        // Build entries with current afterSeq values. Resume from the highest
        // observed live seq (not just the window tail): while scrolled away from
        // the tail, the window tail lags the live tail, and resuming there would
        // make the worker replay a page of messages the live-append guard drops.
        const agents = agentEntries.map((entry) => {
          const resumeSeq = untrack(() => chatStore.getResumeAfterSeq(entry.agentId))
          // Capture the resume cursor as the CatchUpStart reap ceiling (see catchUpStart).
          resumeTails.set(entry.agentId, resumeSeq)
          return agentWatchEntry(entry.agentId, resumeSeq)
        })

        const workerId = untrack(() => params.getWorkerId())
        if (!workerId)
          return

        // Seed after_offset from the tab's resume cursor; 0 means a
        // cold subscribe (the tab was hydrated without a screen or the
        // cursor hasn't advanced yet).
        const terminals = terminalIds.map(id => ({
          terminalId: id,
          afterOffset: BigInt(untrack(() => metadata.get(id)?.lastOffset ?? 0)),
        }))

        // Open the E2EE channel stream to the Worker.
        const handle = await watchEventsViaChannel(workerId, {
          agents,
          terminals,
        })

        // Teardown may have aborted while we awaited the async channel open. The
        // handle is already live (its stream listener is registered on open), so
        // close it before wiring callbacks -- otherwise a superseded or torn-down
        // subscription keeps firing store mutations, and on unmount previousHandle
        // was already nulled, so nothing else would ever close it. Mirrors the
        // disposed-check workspacePrivateEvents runs after its own channel open.
        if (signal.aborted) {
          handle.close()
          return
        }

        // Reset catch-up phases only after the replacement stream exists. workerRpc
        // buffers events until onEvent is wired below, so a pre-CatchUpStart live frame
        // cannot sneak through without the guard; a failed open no longer leaves the
        // store permanently in catching-up mode.
        for (const entry of agentEntries) {
          catchUpPhases.set(entry.agentId, 'catchingUp')
          if (!nonActiveAgentIds.has(entry.agentId))
            chatStore.setCatchingUp(entry.agentId, true)
        }

        // Wire the consumer callbacks before closing the previous handle.
        // workerRpc.ts buffers any events that arrive before onEvent is
        // wired; waitForStreamCompletion captures end / error / abort that
        // fire during the synchronous setup window.
        handle.onEvent((response) => {
          backoff.reset(BACKOFF_KEY)
          switch (response.event.case) {
            case 'agentEvent':
              handleAgentEvent(response.event.value, catchUpPhases, resumeTails)
              break
            case 'terminalEvent':
              handleTerminalEvent(response.event.value)
              break
          }
        })

        // Now that callbacks are wired, clean up the previous stream.
        // The server-side sender update ensures no more events arrive on
        // the old request ID once the server processes this WatchEvents.
        previousHandle?.close()
        previousHandle = handle

        await waitForStreamCompletion(handle, signal)
      }
      catch (err) {
        if (signal.aborted)
          return

        const isConnectionLost = err instanceof ChannelError && err.source === 'transport'

        if (isConnectionLost) {
          showWarnToast('Connection to worker lost, reconnecting\u2026', err)
          // Channel disconnected (worker went offline or restarted).
          // Mark worker as offline so terminals show disconnection and
          // thinking indicators are hidden.
          setWorkerOnline(false)
        }
        else {
          // Stream-level error (e.g. NOT_FOUND for entities not yet
          // visible). Retry quickly without alarming the user. Reset
          // the backoff so a benign transient error doesn't inherit a
          // long delay from a prior connection-lost streak.
          log.warn('[watchEvents] stream error, retrying:', err)
          backoff.reset(BACKOFF_KEY)
        }
      }

      if (signal.aborted)
        return
      await new Promise<void>((resolve) => {
        backoff.schedule(BACKOFF_KEY, resolve)
      })
    }
  }

  // Watch all agents and terminals on the current worker via a single
  // unified WatchEvents stream. When the entity set changes (new agent
  // or terminal created), the effect triggers a stream restart.
  // Covers agents/terminals in EVERY workspace, not just the active one, so
  // status updates keep flowing for tabs the user is not currently looking at.
  createEffect(() => {
    const workerId = params.getWorkerId()
    const wsId = params.getActiveWorkspaceId()

    // Collect all agent IDs on this worker.
    const agentEntries: WatchAgentEntry[] = []
    const terminalIds: string[] = []
    // Per-RUN locals, not hook-scoped state. They are a snapshot of one pass
    // over the view, and the only consumer that outlives the pass is the async
    // `watchEvents` body -- which now receives the snapshot as an argument.
    const nonActiveAgentIds = new Set<string>()
    const nonActiveTerminalIds = new Set<string>()

    if (wsId && workerId) {
      // One pass over every tab on this worker, in ANY workspace. There used
      // to be two: the active workspace from `tabStore`, then every other
      // workspace from its registry snapshot, with the second pass recording
      // which ids were "non-active" so the handlers could branch. The view
      // spans all workspaces, and each tab carries the workspace it belongs
      // to, so the partition is a read rather than bookkeeping.
      for (const tab of params.view.all()) {
        if (tab.workerId !== workerId)
          continue
        const inActive = tab.workspaceId === wsId
        if (tab.type === TabType.AGENT) {
          // Seed entry: the per-subscribe build (see agents.map above) recomputes
          // replay/cursor from the live resume cursor, so the placeholder is fresh.
          agentEntries.push(agentWatchEntry(tab.id, BigInt(0)))
          if (!inActive)
            nonActiveAgentIds.add(tab.id)
        }
        else if (tab.type === TabType.TERMINAL) {
          terminalIds.push(tab.id)
          if (!inActive)
            nonActiveTerminalIds.add(tab.id)
        }
      }
    }

    // Build a key representing the current subscription set and each target's role.
    // Role matters: the same id moving between active and non-active handling must
    // restart the stream so callbacks stop dropping full chat/control processing.
    const newKey = buildWatchTargetsKey(workerId, agentEntries, terminalIds, nonActiveAgentIds, nonActiveTerminalIds)

    // Skip if the subscription set hasn't changed.
    if (newKey === currentTargetsKey)
      return

    // Tear down old stream.
    if (eventStreamAbort) {
      eventStreamAbort.abort()
      eventStreamAbort = null
    }
    currentTargetsKey = newKey

    // Nothing left to watch.
    if (!workerId || (agentEntries.length === 0 && terminalIds.length === 0)) {
      if (workerId) {
        // currentTargetsKey is the generation: this effect assigned newKey to
        // it just above, so any later run that changes the watch set moves it
        // and abandons this unsubscribe before it can wipe the newer one.
        void unsubscribeAllWatchEvents(workerId, () => currentTargetsKey === newKey)
      }
      previousHandle?.close()
      previousHandle = null
      return
    }

    const abort = new AbortController()
    eventStreamAbort = abort
    watchEvents(agentEntries, terminalIds, nonActiveAgentIds, abort.signal)
  })

  // When the worker goes offline, mark running terminals as disconnected,
  // clear stale streaming text, and set active agents to inactive so the
  // thinking indicator hides. The real status will arrive when the WatchEvents
  // stream reconnects.
  createEffect(() => {
    if (workerOnline())
      return
    const workerId = params.getWorkerId()
    // `untrack`, and the reads resolved BEFORE the writes. This body reads the
    // join (which reads `tabMetadata`) and then writes `tabMetadata`, so
    // without `untrack` the effect subscribes to what it just mutated and runs
    // the whole sweep a second time. Resolving the matches up front matters
    // separately: `view.getTerminalTab` inside `patchMatching`'s predicate is a
    // memo read, and reading it mid-`produce` forces the join to recompute
    // after every write the same `produce` has already made -- O(N) full
    // re-joins for N disconnected terminals. `syncGitStatusToTabs` and
    // `workspaceSwitcher` guard the identical shape the same way.
    untrack(() => {
      const { terminals: affectedTerminals, agents } = collectWorkerOfflineTargets(params.view.all(), workerId)
      batch(() => {
        // One call reaches every workspace. This used to be the active store's
        // sweep plus a hand-rolled fan-out over each registry snapshot.
        if (affectedTerminals.size > 0) {
          params.metadata.patchMatching(
            (_meta, tabId) => affectedTerminals.has(tabId),
            { terminalStatus: TerminalStatus.DISCONNECTED },
          )
        }
        for (const tab of agents) {
          chatStore.streamingText.clear(tab.id)
          for (const spanId of Object.keys(chatStore.getAgentCommandStreams(tab.id)))
            chatStore.clearCommandStream(tab.id, spanId)
          if (tab.agentStatus === AgentStatus.ACTIVE)
            metadata.patch(tab.id, { agentStatus: AgentStatus.INACTIVE })
        }
      })
    })
  })

  // Lazy message loading for agent tabs not on the current worker
  createEffect(() => {
    const activeKey = selection.activeKeyForWorkspace(params.getActiveWorkspaceId() ?? '')
    if (!activeKey)
      return
    // `parseTabKey`, not a hand-rolled split: it is the documented inverse of
    // `tabKey` (already imported here), matches on the FIRST colon rather than
    // rejecting any key containing a second, and carries the `Number.isInteger`
    // guard this site lacked.
    const parsed = parseTabKey(activeKey)
    if (!parsed || parsed.type !== TabType.AGENT)
      return
    const tabId = parsed.id
    if (chatStore.isInitialLoadComplete(tabId))
      return
    // A tab whose `worker_id` register has not resolved yet cannot be loaded:
    // the RPC needs a worker to address, and an empty one comes back as
    // "invalid_argument". It will be retried when the register lands.
    const agent = view.getAgentTab(tabId)
    if (!agent || !agent.workerId)
      return
    chatStore.loadInitialMessages(agent.workerId, tabId).catch((err) => {
      showWarnToast('Failed to load chat history', err)
    })
    // Seed the scroll-rail marks for the newly-opened agent (fire-and-forget). Tied to the
    // current subscription's signal so a resubscribe/teardown cancels it (see loadMessageMarks).
    void chatStore.loadMessageMarks(agent.workerId, tabId, eventStreamAbort?.signal)
  })

  // Continuous tail reconcile: whenever a loaded window lags its recorded live tail
  // while the reader is AT the tail (hasMoreNewer false but NOT caught up), forward-fill
  // the gap. This REPLACES the one-shot forward-fill that fired on catchUpComplete when
  // replay_has_more: keying on the windowing invariant rather than a discrete frame
  // makes it (a) fire for ANY cause of the lag -- a bounded catch-up replay OR a live
  // arrival the store dropped beyond the window to keep it contiguous
  // (beyondUnloadedNewerTail) -- and (b) robust to a stream drop between the catch-up
  // status marker and CatchUpComplete (the gap closes on the next reactive change, not
  // only on a successful CatchUpComplete). catchUpToTail is idempotent (no-ops while one
  // is already draining the agent) and its loop exits the moment the window catches up
  // or the reader scrolls away, so a re-run on its own per-page writes does no work.
  createEffect(() => reconcileLaggingTails({
    agentTabs: () => params.view.all()
      .filter(t => t.type === TabType.AGENT)
      .map(t => ({ id: t.id, workerId: t.workerId ?? '' })),
    hasNewerMessages: id => chatStore.hasNewerMessages(id),
    caughtUpToLiveTail: id => chatStore.caughtUpToLiveTail(id),
    isTailFillDeferred: id => chatStore.isTailFillDeferred(id),
    getLastSeq: id => chatStore.getLastSeq(id),
    isFetchingNewer: id => chatStore.isFetchingNewer(id),
    // Tie all three reconcile-driven forward-fill paths to the CURRENT WatchEvents
    // subscription, so a workspace switch / worker change (which aborts + replaces
    // eventStreamAbort) stops a fetch running against a worker the reader navigated
    // away from instead of leaking it. They are single-flight + idempotent, so a
    // resubscribe that aborts one simply restarts it on the next reconcile tick -- the
    // pre-windowing teardown guarantee, restored. The empty-window re-seat
    // (jumpToLatest) ties via the store's beginHistoryFetch (its fetch + the
    // forwardFillToLiveTail loop it drives both abort with the signal).
    catchUpToTail: (workerId, agentId, afterSeq) => void chatStore.catchUpToTail(workerId, agentId, afterSeq, eventStreamAbort?.signal),
    resumeDeferredTailFill: (workerId, agentId) => void chatStore.resumeDeferredTailFill(workerId, agentId, eventStreamAbort?.signal),
    jumpToLatest: (workerId, agentId) => void chatStore.jumpToLatestMessages(workerId, agentId, eventStreamAbort?.signal),
  }))

  // Abort the stream on page unload. SolidJS's onCleanup does
  // not fire on hard browser refresh, so without this the connection stays
  // open as a zombie until the server times it out.
  const abortStream = () => {
    if (eventStreamAbort) {
      eventStreamAbort.abort()
      eventStreamAbort = null
    }
    previousHandle?.close()
    previousHandle = null
  }

  window.addEventListener('beforeunload', abortStream)

  onCleanup(() => {
    window.removeEventListener('beforeunload', abortStream)
    abortStream()
    currentTargetsKey = ''
  })

  return {
    workerOnline,
  }
}
