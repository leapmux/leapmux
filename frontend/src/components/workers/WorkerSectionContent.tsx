import type { Component } from 'solid-js'
import type { TunnelInfo } from '~/api/platformBridge'
import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'
import type { WorkerInfo } from '~/lib/workerInfoCache'
import type { ChannelStatus } from '~/stores/workerChannelStatus.store'
import ArrowBigRightDash from 'lucide-solid/icons/arrow-big-right-dash'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import ChevronsLeftRightEllipsis from 'lucide-solid/icons/chevrons-left-right-ellipsis'
import { createEffect, createMemo, createSignal, For, Show } from 'solid-js'
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
  onAddTunnel: (worker: Worker) => void
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

export const WorkerSectionContent: Component<WorkerSectionContentProps> = (props) => {
  const auth = useAuth()
  const tunnel = useTunnel()
  const [collapsedIds, setCollapsedIds] = createSignal<Set<string>>(new Set())
  // Each delete confirmation captures the identity scope it was opened for.
  // A plain snapshot taken when the confirmation opens, never a reactive read,
  // so a later identity cannot rewrite what the confirmation admits.
  const [deleteTunnelTarget, setDeleteTunnelTarget] = createSignal<{ tunnel: TunnelInfo, identityScope: string } | null>(null)
  const [deleteAllTunnelsTarget, setDeleteAllTunnelsTarget] = createSignal<{ workerId: string, identityScope: string } | null>(null)
  // The delete confirmation stays mounted across the removal, so it owns the
  // removal's error and busy state rather than closing on the click and
  // swallowing the outcome.
  const [deleteError, setDeleteError] = createSignal<string | null>(null)
  const [deleting, setDeleting] = createSignal(false)
  // Per-tunnel verification state for stopped rows: one busy flag, and one
  // error attributed to the row that asked — two stopped rows may show their
  // own blocks at once, and one failure must not appear in both.
  const [verifyingTunnelId, setVerifyingTunnelId] = createSignal<string | null>(null)
  const [verifyError, setVerifyError] = createSignal<{ id: string, message: string } | null>(null)

  function identityIsLive(scope: string): boolean {
    return (auth.user()?.id ?? '') === scope
  }

  // Identity retirement ends any open confirmation: a sign-out, or a sign-in
  // as someone else, leaves the submission belonging to an identity that is
  // gone.
  createEffect(() => {
    auth.user() // track
    const single = deleteTunnelTarget()
    if (single && !identityIsLive(single.identityScope)) {
      setDeleteTunnelTarget(null)
      setDeleteError(null)
    }
    const all = deleteAllTunnelsTarget()
    if (all && !identityIsLive(all.identityScope)) {
      setDeleteAllTunnelsTarget(null)
      setDeleteError(null)
    }
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

  async function confirmDeleteTunnel() {
    const target = deleteTunnelTarget()
    if (!target || deleting())
      return
    // Reject a stale submission before dispatching: the confirmation belongs
    // to an identity this document no longer holds.
    if (!identityIsLive(target.identityScope)) {
      setDeleteTunnelTarget(null)
      setDeleteError(null)
      return
    }
    setDeleting(true)
    setDeleteError(null)
    try {
      await tunnel!.remove(target.tunnel.id)
      setDeleteTunnelTarget(null)
    }
    catch (err) {
      setDeleteError(formatErrorMessage(err, 'Could not delete the tunnel. Try again.'))
    }
    finally {
      setDeleting(false)
    }
  }

  async function confirmDeleteAllTunnels() {
    const target = deleteAllTunnelsTarget()
    if (!target || deleting())
      return
    // Reject a stale submission before dispatching: the confirmation belongs
    // to an identity this document no longer holds.
    if (!identityIsLive(target.identityScope)) {
      setDeleteAllTunnelsTarget(null)
      setDeleteError(null)
      return
    }
    setDeleting(true)
    setDeleteError(null)
    try {
      await tunnel!.removeAllForWorker(target.workerId)
      setDeleteAllTunnelsTarget(null)
    }
    catch (err) {
      setDeleteError(formatErrorMessage(err, 'Could not delete the tunnels. Try again.'))
    }
    finally {
      setDeleting(false)
    }
  }

  // The verification is the browser channel path: it prompts for a changed
  // key and records the accepted one, which is what a recreation admits
  // against. The outcome is shown beside the row that asked for it.
  function verify(t: TunnelInfo) {
    if (verifyingTunnelId())
      return
    setVerifyingTunnelId(t.id)
    setVerifyError(null)
    void verifyWorkerKey(t.workerId)
      .catch((err: unknown) => {
        setVerifyError({
          id: t.id,
          message: formatErrorMessage(err, 'Could not verify the Worker key.'),
        })
      })
      .finally(() => {
        setVerifyingTunnelId(null)
      })
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
                        onAddTunnel={() => props.onAddTunnel(worker)}
                        onDeleteAllTunnels={() => {
                          setDeleteError(null)
                          setDeleteAllTunnelsTarget({ workerId: worker.id, identityScope: auth.user()?.id ?? '' })
                        }}
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
                                    onDelete={() => {
                                      setDeleteError(null)
                                      setDeleteTunnelTarget({ tunnel: t, identityScope: auth.user()?.id ?? '' })
                                    }}
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
                                      Stopped: the Worker keys the Hub announced do not match the key this tunnel admits against.
                                    </div>
                                    <div class={styles.tunnelFingerprints}>
                                      <div>
                                        <strong>Expected:</strong>
                                        {' '}
                                        <code data-testid="tunnel-expected-fingerprint">{mismatch().expectedFingerprint}</code>
                                      </div>
                                      <div>
                                        <strong>Presented:</strong>
                                        {' '}
                                        <code data-testid="tunnel-presented-fingerprint">{mismatch().presentedFingerprint}</code>
                                      </div>
                                    </div>
                                    <Show when={verifyError()?.id === t.id
                                      ? verifyError()!.message
                                      : null}
                                    >
                                      {message => (
                                        <div class={errorText} role="alert" data-testid="tunnel-verify-error">{message()}</div>
                                      )}
                                    </Show>
                                    <div class={styles.tunnelStoppedActions}>
                                      <button
                                        type="button"
                                        class="outline"
                                        disabled={verifyingTunnelId() !== null}
                                        onClick={() => verify(t)}
                                        data-testid="tunnel-verify-key"
                                      >
                                        <Show when={verifyingTunnelId() === t.id}><Spinner /></Show>
                                        Verify Worker key
                                      </button>
                                      <button
                                        type="button"
                                        class="outline"
                                        data-variant="danger"
                                        onClick={() => {
                                          setDeleteError(null)
                                          setDeleteTunnelTarget({ tunnel: t, identityScope: auth.user()?.id ?? '' })
                                        }}
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

      <Show when={deleteTunnelTarget()}>
        {target => (
          <ConfirmDialog
            title="Delete tunnel"
            danger
            busy={deleting()}
            confirmLabel="Delete"
            confirmTestId="confirm-delete-tunnel"
            onConfirm={confirmDeleteTunnel}
            onCancel={() => {
              setDeleteTunnelTarget(null)
              setDeleteError(null)
            }}
          >
            <p>
              {'Delete tunnel '}
              <strong>{tunnelLabel(target().tunnel)}</strong>
              ?
            </p>
            <Show when={deleteError()}>
              <p class={errorText} role="alert" data-testid="tunnel-delete-error">{deleteError()}</p>
            </Show>
          </ConfirmDialog>
        )}
      </Show>

      <Show when={deleteAllTunnelsTarget()}>
        {target => (
          <ConfirmDialog
            title="Delete all tunnels"
            danger
            busy={deleting()}
            confirmLabel="Delete all"
            confirmTestId="confirm-delete-all-tunnels"
            onConfirm={confirmDeleteAllTunnels}
            onCancel={() => {
              setDeleteAllTunnelsTarget(null)
              setDeleteError(null)
            }}
          >
            <p>
              {'Delete all tunnels for worker '}
              <strong>{props.workerInfo(target().workerId)?.name ?? target().workerId}</strong>
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
