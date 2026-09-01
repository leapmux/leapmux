import type { Component } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { SessionIdState } from '~/hooks/createSessionIdState'
import type { UseResumableSessionsArgs } from '~/hooks/useResumableSessions'
import { Show } from 'solid-js'
import { LabeledField } from '~/components/common/LabeledField'
import { RefreshButton } from '~/components/common/RefreshButton'
import { RESUME_SESSION_ERROR_ID, RESUME_SESSION_LABEL } from '~/components/shell/resumeSession'
import { SessionIdInput } from '~/components/shell/SessionIdInput'
import { SessionSelect } from '~/components/shell/SessionSelect'
import { useResumableSessions } from '~/hooks/useResumableSessions'

interface ResumeSessionFieldProps {
  state: SessionIdState
  workerId: string
  workingDir: string
  agentProvider: AgentProvider | undefined
}

/**
 * The "resume an existing session" field of the New Agent and New Workspace
 * dialogs.
 *
 * It picks between two controls, and the rule is ONE condition: a menu when the
 * worker offered sessions, and a text input when it offered none.
 *
 * The menu is the point of the field. Typing a session id from memory is what
 * made resume error-prone -- a typo either failed validation or, for a provider
 * whose handle is a path, silently opened an empty session at a filename that
 * does not exist.
 *
 * The text input survives as the FALLBACK, and deleting it would remove a
 * capability. The list can only ever hold what this worker can enumerate: a
 * session started on another machine, a store the CLI moved, a provider whose
 * store this worker cannot read, and a worker that failed to answer at all
 * would each leave the user with a menu they cannot resume anything from.
 * `useResumableSessions` reports every one of those as an empty list, which is
 * why the swap needs one condition rather than a separate error path.
 *
 * The FRAME is shared and only the control swaps, which is what makes the
 * refresh button reachable. A failed fetch is exactly the case that mounts the
 * text input, so a button that lived with the menu would be absent whenever it
 * was the thing to press.
 *
 * `createSessionIdState` keeps validating either way. A handle picked from the
 * menu passes it -- the worker issued it -- so the gate costs the menu nothing
 * and stays the guard on the path where a human types.
 */
export const ResumeSessionField: Component<ResumeSessionFieldProps> = (props) => {
  // Null until all three are known. Asking for the sessions of an empty
  // directory would make the worker scan every provider store it has.
  const source = (): UseResumableSessionsArgs | null => {
    if (!props.workerId || !props.workingDir || props.agentProvider === undefined)
      return null
    return {
      workerId: props.workerId,
      workingDir: props.workingDir,
      agentProvider: props.agentProvider,
    }
  }

  const { sessions, loading, refresh } = useResumableSessions(source)

  // The menu also holds the field while the list is on its way, so the control
  // does not swap under the user between the dialog opening and the answer
  // arriving.
  const showMenu = () => loading() || sessions().length > 0

  return (
    <LabeledField
      label={RESUME_SESSION_LABEL}
      /*
        The error travels with the field, not with either control.

        A handle the menu offers always validates, so this row is normally
        absent -- but the value SURVIVES the swap. A user who typed an invalid
        handle while the list was empty, and then changed the directory to one
        that has sessions, would otherwise face a disabled Create button, their
        own text on the trigger, and no statement of what is wrong.
      */
      error={props.state.error()}
      errorId={RESUME_SESSION_ERROR_ID}
      /*
        The only route to `useResumableSessions().refresh()`, and the reason
        `ShellSelector` grew the same button: the hook's effect re-fetches on a
        CHANGE of worker, directory or provider, so a transient failure against
        the current three leaves the field with no list and no way back except
        picking another directory and returning.

        Disabled while a fetch is in flight, and while the field does not yet
        know all three -- pressing it then would do nothing at all.
      */
      actions={(
        <RefreshButton
          onClick={() => void refresh()}
          disabled={loading() || source() === null}
          title="Refresh sessions"
          data-testid="session-field-refresh"
        />
      )}
    >
      <Show when={showMenu()} fallback={<SessionIdInput state={props.state} />}>
        <SessionSelect
          value={props.state.trimmed()}
          onChange={props.state.setValue}
          sessions={sessions()}
          loading={loading()}
        />
      </Show>
    </LabeledField>
  )
}
