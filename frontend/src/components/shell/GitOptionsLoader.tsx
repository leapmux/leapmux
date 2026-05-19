import type { JSX } from 'solid-js'
import type { GitPathInfo } from '~/hooks/useGitPathInfo'
import { Show } from 'solid-js'
import { Spinner } from '~/components/common/Spinner'

interface GitOptionsLoaderProps {
  gitInfo: GitPathInfo
  /** Rendered while the initial probe is in flight. */
  children: () => JSX.Element
}

/**
 * Render-prop gate that wires `useGitPathInfo` to the standard dialog
 * spinner / GitOptions render. Renders the spinner during the probe and
 * the children only when `showGitOptions()` flips true, matching the
 * pattern that was duplicated across the three worker dialogs.
 *
 * When the worker returns IsGitRepo=false with an `errorHint` populated
 * (dubious-ownership, EACCES, transient I/O), render an inline
 * diagnostic warning so the user can act on it. Earlier revisions
 * routed every non-errNotGitRepo failure through `sendInternalError`,
 * which surfaced as a generic "Internal error" toast with no
 * actionable text; the worker now ships the diagnostic in
 * GetGitInfoResponse.error_hint so the dialog can render it in place
 * of (or alongside) the silent non-git fallback.
 */
export function GitOptionsLoader(props: GitOptionsLoaderProps): JSX.Element {
  return (
    <>
      <Show when={props.gitInfo.loading()}>
        <p>
          <Spinner />
          {' '}
          Loading branch info
        </p>
      </Show>
      <Show when={!props.gitInfo.loading() && !props.gitInfo.showGitOptions() && props.gitInfo.info().errorHint}>
        <p role="alert">
          Git probe failed:
          {' '}
          {props.gitInfo.info().errorHint}
        </p>
      </Show>
      <Show when={!props.gitInfo.loading() && props.gitInfo.showGitOptions()}>
        {props.children()}
      </Show>
    </>
  )
}
