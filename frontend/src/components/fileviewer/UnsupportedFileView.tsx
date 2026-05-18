import type { Component, JSX } from 'solid-js'
import type { ActiveOp } from '~/components/common/fileSaveActions'
import type { PathFlavor } from '~/lib/paths'
import { createMemo, createUniqueId, Show } from 'solid-js'
import { StartupBody, StartupSpinner } from '~/components/common/StartupPanel'
import { formatBytes } from '~/lib/formatBytes'
import { basename } from '~/lib/paths'
import * as styles from './FileViewer.css'

// Save-button content: idle label, or spinner-plus-percent when this
// button's op is in flight. Three call sites in this file (Download,
// Save as, Save to Downloads) only differ in the
// `active`/`label`/`busyLabel` triple.
function SaveButtonContent(props: {
  active: boolean
  label: string
  busyLabel: string
  progress: number | null
}): JSX.Element {
  return (
    <Show when={props.active} fallback={<>{props.label}</>}>
      <StartupSpinner label={props.progress === null ? props.busyLabel : `${props.busyLabel} ${props.progress}%`} />
    </Show>
  )
}

export type UnsupportedReason = 'binary' | 'oversize-image'

/**
 * Desktop-mode save controls: render two save buttons (Save as / Save
 * to Downloads) and a "reveal in file manager" checkbox below.
 *
 * The in-flight save (or null) is read from the parent `op` prop —
 * both buttons disable while any save runs, but only the active one
 * (matching `op.kind`) shows the spinner. Web-mode callers leave this
 * slot undefined and pass `onDownload` instead, yielding a single
 * anchor-click Download button.
 */
export interface DesktopSaveControls {
  onSaveAs: () => void
  onSaveToDownloads: () => void
  revealAfterDownload: boolean
  onRevealAfterDownloadChange: (value: boolean) => void
}

export interface UnsupportedFileViewProps {
  filePath: string
  flavor: PathFlavor
  totalSize: number
  reason: UnsupportedReason
  loadingAnyway: boolean
  /**
   * When false, the secondary "Show anyway" button is omitted. Used to
   * hide the action for empty files (nothing to preview).
   */
  canShowAnyway: boolean
  onShowAnyway: () => void
  /**
   * In-flight save/download state, or `null` when idle. Drives the
   * per-button spinner (`op.kind` selects which button is active) and
   * the percent label suffix (`op.progress` becomes "... 45%" when
   * non-null; `null` keeps the bare spinner — used during the save-as
   * dialog and the first worker round-trip before the total is known).
   * Both web and desktop variants share this state; the web variant
   * only ever sees `kind === 'download'`.
   */
  op: ActiveOp | null
  onDownload: () => void
  // Desktop variant: when present, replaces the single "Download" with
  // "Save as..." / "Save to Downloads" and shows the reveal checkbox.
  desktop?: DesktopSaveControls
}

function titleFor(reason: UnsupportedReason): string {
  if (reason === 'oversize-image')
    return 'This image is too large to preview.'
  return 'This file cannot be displayed inline.'
}

/**
 * GitHub-style fallback view for files we cannot render inline.
 *
 * Layout mirrors the agent/terminal startup-failure pane:
 *   - <h2> title via StartupBody
 *   - filename · size meta line
 *   - action row (Download or Save buttons + Show anyway)
 *   - reveal checkbox below the action row (desktop only)
 */
export const UnsupportedFileView: Component<UnsupportedFileViewProps> = (props) => {
  const name = createMemo(() => basename(props.filePath, props.flavor))
  const titleId = createUniqueId()
  const revealId = createUniqueId()
  const busy = () => props.op !== null
  const progress = () => props.op?.progress ?? null

  return (
    <div
      class={styles.unsupportedPane}
      role="region"
      aria-labelledby={titleId}
      data-testid="unsupported-file-view"
    >
      <StartupBody
        title={titleFor(props.reason)}
        titleId={titleId}
        body={(
          <p class={styles.unsupportedMeta} data-testid="unsupported-meta">
            {`${name()} · ${formatBytes(props.totalSize)}`}
          </p>
        )}
      >
        <Show
          when={props.desktop}
          fallback={(
            <button
              type="button"
              data-testid="unsupported-download-button"
              aria-label={`Download ${name()}`}
              disabled={busy()}
              onClick={() => props.onDownload()}
            >
              <SaveButtonContent
                active={props.op?.kind === 'download'}
                label="Download"
                busyLabel="Downloading..."
                progress={progress()}
              />
            </button>
          )}
        >
          {desktop => (
            <>
              <button
                type="button"
                data-testid="unsupported-save-as-button"
                aria-label={`Save ${name()} as...`}
                disabled={busy()}
                onClick={() => desktop().onSaveAs()}
              >
                <SaveButtonContent
                  active={props.op?.kind === 'save-as'}
                  label="Save as..."
                  busyLabel="Saving..."
                  progress={progress()}
                />
              </button>
              <button
                type="button"
                class="outline"
                data-testid="unsupported-save-to-downloads-button"
                aria-label={`Save ${name()} to Downloads`}
                disabled={busy()}
                onClick={() => desktop().onSaveToDownloads()}
              >
                <SaveButtonContent
                  active={props.op?.kind === 'save-to-downloads'}
                  label="Save to Downloads"
                  busyLabel="Saving..."
                  progress={progress()}
                />
              </button>
            </>
          )}
        </Show>
        <Show when={props.canShowAnyway}>
          <button
            type="button"
            class="outline"
            data-testid="unsupported-show-anyway-button"
            aria-label={`Show ${name()} anyway`}
            disabled={props.loadingAnyway}
            onClick={() => props.onShowAnyway()}
          >
            <Show when={props.loadingAnyway} fallback={<>Show anyway</>}>
              <StartupSpinner label="Loading..." />
            </Show>
          </button>
        </Show>
      </StartupBody>
      <Show when={props.desktop}>
        {desktop => (
          <label
            class={styles.unsupportedRevealRow}
            for={revealId}
            data-testid="unsupported-reveal-checkbox-label"
          >
            <input
              id={revealId}
              type="checkbox"
              data-testid="unsupported-reveal-checkbox"
              checked={desktop().revealAfterDownload}
              onChange={e => desktop().onRevealAfterDownloadChange(e.currentTarget.checked)}
            />
            Reveal in file manager after save
          </label>
        )}
      </Show>
    </div>
  )
}
