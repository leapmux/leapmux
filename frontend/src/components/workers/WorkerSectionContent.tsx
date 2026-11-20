import type { Component } from 'solid-js'
import type { AddTunnelTarget } from './AddTunnelDialog'
import type { TunnelInfo } from '~/api/platformBridge'
import type { WorkerTrustScope } from '~/context/AuthContext'
import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import ArrowBigRightDash from 'lucide-solid/icons/arrow-big-right-dash'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import ChevronsLeftRightEllipsis from 'lucide-solid/icons/chevrons-left-right-ellipsis'
import { createEffect, createMemo, createSignal, For, onCleanup, Show } from 'solid-js'
import { ClippedText } from '~/components/common/ClippedText'
import { ConfirmDialog } from '~/components/common/ConfirmDialog'
import { createContextMenuAnchor } from '~/components/common/DropdownMenu'
import { Spinner } from '~/components/common/Spinner'
import { StatusDot } from '~/components/common/StatusDot'
import * as shared from '~/components/tree/sharedTree.css'
import { actionSlot, actionSlotResting, sidebarActions } from '~/components/tree/sidebarActions.css'
import * as listStyles from '~/components/workspace/workspaceList.css'
import { useAuth } from '~/context/AuthContext'
import { useTunnel } from '~/context/TunnelContext'
import { formatErrorMessage } from '~/lib/errors'
import { errorText } from '~/styles/shared.css'
import { TunnelContextMenu } from './TunnelContextMenu'
import { verifyWorkerKey } from './verifyWorkerKey'
import { WorkerContextMenu } from './WorkerContextMenu'
import * as styles from './workerSection.css'

export interface WorkerSectionContentProps {
  workers: Worker[]
  workerInfo: (id: string) => WorkerInfo | null
  channelStatus: (id: string) => ChannelStatus
  onAddTunnel: (target: AddTunnelTarget) => void
  onDeregister: (worker: Worker) => void
}

const statusClass: Record<ChannelStatus, string> = {
  connected: styles.statusConnected,
  disconnected: styles.statusDisconnected,
}

/** What the dot's colour means. The dot has no other way to say it. */
const statusLabel: Record<ChannelStatus, string> = {
  connected: 'Connected',
  disconnected: 'Disconnected',
}

type DeleteSelection = { kind: 'one', tunnel: TunnelInfo } | { kind: 'all', workerId: string }
type DeleteTarget = DeleteSelection & { scope: WorkerTrustScope }
interface TunnelReview {
  id: string
  scope: WorkerTrustScope
}

export const WorkerSectionContent: Component<WorkerSectionContentProps> = (props) => {
  const auth = useAuth()
  const tunnel = useTunnel()
  const [collapsedIds, setCollapsedIds] = createSignal<Set<string>>(new Set())
  const [deleteTarget, setDeleteTarget] = createSignal<DeleteTarget | null>(null)
  const [deleteError, setDeleteError] = createSignal<string | null>(null)
  const [deleting, setDeleting] = createSignal(false)
  const [review, setReview] = createSignal<TunnelReview | null>(null)
  const [verifyError, setVerifyError] = createSignal<(TunnelReview & { message: string }) | null>(null)
  let reviewLifetime: AbortController | undefined
  let disposed = false
  onCleanup(() => {
    disposed = true
    reviewLifetime?.abort()
  })

  function identityIsLive(scope: WorkerTrustScope): boolean {
    return !disposed && auth.workerTrustScope() === scope && !scope.signal.aborted
  }

  function closeDelete(target: DeleteTarget): void {
    if (deleteTarget() !== target)
      return
    setDeleteTarget(null)
    setDeleteError(null)
    setDeleting(false)
  }

  function openDelete(selection: DeleteSelection): void {
    const scope = auth.workerTrustScope()
    if (!scope.userId || !identityIsLive(scope) || deleting())
      return
    setDeleteError(null)
    setDeleteTarget({ ...selection, scope })
  }

  createEffect(() => {
    const target = deleteTarget()
    if (target && !identityIsLive(target.scope))
      closeDelete(target)
    const activeReview = review()
    if (activeReview && (!identityIsLive(activeReview.scope)
      || !tunnel?.tunnels().some(row => row.id === activeReview.id && row.keyMismatch))) {
      reviewLifetime?.abort()
      setReview(null)
    }
    const failure = verifyError()
    if (failure && !identityIsLive(failure.scope))
      setVerifyError(null)
  })

  function isExpanded(id: string): boolean {
    return !collapsedIds().has(id)
  }

  function toggleExpanded(id: string) {
    setCollapsedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id))
        next.delete(id)
      else
        next.add(id)
      return next
    })
  }

  function tunnelLabel(t: TunnelInfo): string {
    return t.type === 'socks5'
      ? `SOCKS5 ${t.bindAddr}:${t.bindPort}`
      : `${t.bindAddr}:${t.bindPort} \u2192 ${t.targetAddr}:${t.targetPort}`
  }

  async function confirmDelete(target: DeleteTarget): Promise<void> {
    if (deleteTarget() !== target || !identityIsLive(target.scope) || deleting() || !tunnel)
      return
    const isCurrent = () => identityIsLive(target.scope) && deleteTarget() === target
    setDeleting(true)
    setDeleteError(null)
    try {
      if (target.kind === 'one')
        await tunnel.remove(target.tunnel.id, target.scope)
      else
        await tunnel.removeAllForWorker(target.workerId, target.scope)
      if (isCurrent())
        closeDelete(target)
    }
    catch (err) {
      if (isCurrent())
        setDeleteError(formatErrorMessage(err, 'Could not delete the desktop tunnels. Try again.'))
    }
    finally {
      if (isCurrent())
        setDeleting(false)
    }
  }

  async function verify(worker: Worker, stopped: TunnelInfo): Promise<void> {
    const scope = auth.workerTrustScope()
    if (!scope.userId || !identityIsLive(scope) || review())
      return
    const verification = { id: stopped.id, scope }
    reviewLifetime?.abort()
    const lifetime = new AbortController()
    reviewLifetime = lifetime
    setReview(verification)
    setVerifyError(null)
    try {
      const approval = await verifyWorkerKey(worker.id, scope, lifetime.signal)
      if (!identityIsLive(scope)
        || !tunnel?.tunnels().some(row => row.id === stopped.id && row.keyMismatch)) {
        return
      }
      const { type, targetAddr, targetPort, bindAddr, bindPort } = stopped
      props.onAddTunnel({
        worker,
        scope,
        replacement: {
          config: {
            workerId: worker.id,
            type,
            targetAddr,
            targetPort,
            bindAddr,
            bindPort,
          },
          approval,
        },
      })
    }
    catch (err) {
      if (identityIsLive(scope)
        && tunnel?.tunnels().some(row => row.id === stopped.id && row.keyMismatch)) {
        setVerifyError({
          ...verification,
          message: formatErrorMessage(err, 'Could not verify the Worker key.'),
        })
      }
    }
    finally {
      if (reviewLifetime === lifetime)
        reviewLifetime = undefined
      if (!disposed && review() === verification)
        setReview(null)
    }
  }

  return (
    <div class={styles.workerItems} data-testid="worker-list">
      <Show
        when={props.workers.length > 0}
        fallback={<div class={listStyles.emptySection}>No workers</div>}
      >
        <For each={props.workers}>
          {(worker) => {
            const workerTunnels = () => tunnel?.tunnelsForWorker(worker.id) ?? []
            const workerName = createMemo(() => props.workerInfo(worker.id)?.name ?? '\u2014')
            const status = () => props.channelStatus(worker.id)
            // The row element, for its right-click / long-press menu.
            const [rowEl, setRowEl] = createContextMenuAnchor()
            return (
              <>
                <div
                  ref={setRowEl}
                  class={listStyles.item}
                  data-testid="worker-row"
                  onClick={() => toggleExpanded(worker.id)}
                >
                  <ChevronRight
                    size={14}
                    class={`${shared.chevron} ${isExpanded(worker.id)
                      ? shared.chevronExpanded
                      : ''}`}
                  />
                  <ClippedText
                    text={workerName()}
                    class={listStyles.itemTitle}
                    testId="worker-name"
                  />
                  <div class={sidebarActions}>
                    {/* The dot and the three-dot trigger share one cell: the
                        dot rests at the row's right edge and the trigger takes
                        its place on hover, so the row's width never shifts. */}
                    <div class={actionSlot}>
                      {/* No tooltip: `actionSlotResting` is `pointer-events:
                          none`, so a hover on this dot never reaches it. The
                          accessible name carries the state instead. */}
                      <StatusDot
                        class={`${actionSlotResting} ${statusClass[status()]}`}
                        label={statusLabel[status()]}
                        status={status()}
                      />
                      <WorkerContextMenu
                        contextMenuFor={rowEl}
                        workerInfo={props.workerInfo(worker.id)}
                        autoRegistered={worker.autoRegistered}
                        online={worker.online}
                        hasTunnels={workerTunnels().length > 0}
                        onAddTunnel={() => {
                          const scope = auth.workerTrustScope()
                          if (scope.userId && identityIsLive(scope))
                            props.onAddTunnel({ worker, scope })
                        }}
                        onDeleteAllTunnels={() => openDelete({ kind: 'all', workerId: worker.id })}
                        onDeregister={() => props.onDeregister(worker)}
                      />
                    </div>
                  </div>
                </div>
                <Show when={tunnel}>
                  <div class={`${shared.childrenWrapper} ${isExpanded(worker.id)
                    ? shared.childrenWrapperExpanded
                    : ''}`}
                  >
                    <div class={shared.childrenInner}>
                      <For each={workerTunnels()}>
                        {(t) => {
                          const label = tunnelLabel(t)
                          const [tunnelRowEl, setTunnelRowEl] = createContextMenuAnchor()
                          return (
                            <>
                              <div ref={setTunnelRowEl} class={`${shared.node} ${styles.tunnelItem}`}>
                                {t.type === 'socks5'
                                  ? <ChevronsLeftRightEllipsis size={14} class={styles.tunnelIcon} />
                                  : <ArrowBigRightDash size={14} class={styles.tunnelIcon} />}
                                <ClippedText text={label} class={listStyles.itemTitle} />
                                <div class={sidebarActions}>
                                  <TunnelContextMenu
                                    contextMenuFor={tunnelRowEl}
                                    onDelete={() => openDelete({ kind: 'one', tunnel: t })}
                                  />
                                </div>
                              </div>
                              {/* A stopped row keeps its place in the list so the
                                  mismatch that stopped it stays recoverable: the
                                  reason, both fingerprints, and the two ways out. */}
                              <Show when={t.keyMismatch}>
                                {mismatch => (
                                  <div class={styles.tunnelStopped} data-testid="tunnel-stopped">
                                    <div class={styles.tunnelStoppedReason} data-testid="tunnel-stopped-reason">
                                      Stopped. The Hub reported a different Worker key.
                                    </div>
                                    <div class={styles.tunnelFingerprints}>
                                      <div>
                                        <strong>Expected</strong>
                                        {' '}
                                        <code data-testid="tunnel-expected-fingerprint">{mismatch().expectedFingerprint}</code>
                                      </div>
                                      <div>
                                        <strong>Presented</strong>
                                        {' '}
                                        <code data-testid="tunnel-presented-fingerprint">{mismatch().presentedFingerprint}</code>
                                      </div>
                                    </div>
                                    <Show when={verifyError()?.id === t.id && verifyError()?.message}>
                                      {message => (
                                        <div class={errorText} role="alert" data-testid="tunnel-verify-error">{message()}</div>
                                      )}
                                    </Show>
                                    <div>Compare the presented fingerprint with the one the Worker logs when it starts.</div>
                                    <div class={styles.tunnelStoppedActions}>
                                      <button
                                        type="button"
                                        class="outline"
                                        disabled={review() !== null}
                                        onClick={() => void verify(worker, t)}
                                        data-testid="tunnel-verify-key"
                                      >
                                        <Show when={review()?.id === t.id}><Spinner /></Show>
                                        Verify Worker key
                                      </button>
                                      <button
                                        type="button"
                                        class="outline"
                                        data-variant="danger"
                                        onClick={() => openDelete({ kind: 'one', tunnel: t })}
                                        data-testid="tunnel-remove"
                                      >
                                        Remove
                                      </button>
                                    </div>
                                  </div>
                                )}
                              </Show>
                            </>
                          )
                        }}
                      </For>
                    </div>
                  </div>
                </Show>
              </>
            )
          }}
        </For>
      </Show>

      <Show when={deleteTarget()} keyed>
        {target => (
          <ConfirmDialog
            title={target.kind === 'one'
              ? 'Delete tunnel'
              : 'Delete all tunnels'}
            danger
            busy={deleting()}
            confirmLabel={target.kind === 'one'
              ? 'Delete'
              : 'Delete all'}
            confirmTestId={target.kind === 'one'
              ? 'confirm-delete-tunnel'
              : 'confirm-delete-all-tunnels'}
            onConfirm={() => void confirmDelete(target)}
            onCancel={() => closeDelete(target)}
          >
            <p>
              {target.kind === 'one'
                ? 'Delete tunnel '
                : 'Delete all tunnels for Worker '}
              <strong>
                {target.kind === 'one'
                  ? tunnelLabel(target.tunnel)
                  : props.workerInfo(target.workerId)?.name ?? target.workerId}
              </strong>
              ?
            </p>
            <Show when={deleteError()}>
              <p class={errorText} role="alert" data-testid="tunnel-delete-error">{deleteError()}</p>
            </Show>
          </ConfirmDialog>
        )}
      </Show>
    </div>
  )
}
