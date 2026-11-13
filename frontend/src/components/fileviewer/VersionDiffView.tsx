import type { JSX } from 'solid-js'
import type { ShownVersionDiff } from './versionDiff'
import type { MarkdownRenderContext } from '~/components/chat/renderContext'
import type { RenderReadinessScope } from '~/components/chat/renderReadiness'
import type { DiffViewPreference } from '~/context/PreferencesContext'
import { createMemo, Show } from 'solid-js'
import { DiffView } from '~/components/chat/diff'
import { diffTruncationNotice } from '~/components/chat/diff/diffStyles.css'
import { formatBytes } from '~/lib/formatBytes'
import { equalVersionsNotice, versionDiffMounting } from './VersionDiffView.css'

/**
 * The diff of two versions of a file in the file viewer's diff mode.
 *
 * It draws every line, because compareVersions sets the line limit of the diff
 * to Infinity. When the diff stops where the read of one version stops, a notice
 * under it says so. A diff that finds no change states that in place of an empty
 * pane.
 *
 * The lines mount in slices (see DiffView). The diff reports pending to
 * `readiness` until its last line mounts, and it stays invisible and takes no
 * height until then, so that the viewer shows it once, at its full height. A
 * switch between unified and split keeps the old layout in view while the new
 * one mounts, and reports to `readiness` that it replaces its content until
 * the new one shows.
 */
export function VersionDiffView(props: {
  shown: ShownVersionDiff
  view: DiffViewPreference
  filePath: string
  /** The most bytes that the viewer reads of each version. */
  readLimit: number
  /** Where the diff reports that it has not mounted every line yet, and that it replaces the layout that it shows. */
  readiness: RenderReadinessScope
}): JSX.Element {
  const readLimitNote = () => `It reads only the first ${formatBytes(props.readLimit)} of each version.`
  const context = createMemo((): MarkdownRenderContext => ({ readiness: props.readiness }))
  return (
    <Show
      when={props.shown.diff.hunks.length > 0}
      fallback={(
        <div class={equalVersionsNotice} data-testid="equal-versions-notice">
          {props.shown.stopsAtCut
            ? `No changes up to where the viewer stops reading one of the versions. ${readLimitNote()}`
            : 'The two versions have the same text.'}
        </div>
      )}
    >
      <div
        classList={{
          [versionDiffMounting]: props.readiness.pending(),
        }}
        aria-busy={props.readiness.pending()
          ? 'true'
          : undefined}
      >
        <DiffView
          diff={props.shown.diff}
          view={props.view}
          filePath={props.filePath}
          originalFile={props.shown.originalFile}
          context={context()}
        />
        <Show when={props.shown.stopsAtCut}>
          <div class={diffTruncationNotice} data-testid="diff-stop-notice">
            {`The diff stops where the viewer stops reading one of the versions. ${readLimitNote()}`}
          </div>
        </Show>
      </div>
    </Show>
  )
}
