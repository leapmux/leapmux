import type { Component } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { SessionIdState } from '~/hooks/createSessionIdState'
import type { UseResumableSessionsArgs } from '~/hooks/useResumableSessions'
import { createEffect, createSignal, on, Show } from 'solid-js'
import { LabeledField } from '~/components/common/LabeledField'
import { RefreshButton } from '~/components/common/RefreshButton'
import { RESUME_SESSION_ERROR_ID, RESUME_SESSION_LABEL } from '~/components/shell/resumeSession'
import { SessionIdInput } from '~/components/shell/SessionIdInput'
import { SessionSelect, TYPE_A_HANDLE_VALUE } from '~/components/shell/SessionSelect'
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
 * It picks between two controls: a menu of the sessions the worker offered, and
 * a text box for a handle typed by hand.
 *
 * The menu is the point of the field. Typing a session id from memory is what
 * made resume error-prone -- a typo either failed validation or, for a provider
 * whose handle is a path, silently opened an empty session at a filename that
 * does not exist.
 *
 * The text input survives, and deleting it would remove a capability. The list
 * can only ever hold what this worker can enumerate: a session started on
 * another machine, a store the CLI moved, a provider whose store this worker
 * cannot read, a worker that failed to answer, a session already open in a tab,
 * and anything past the worker's cap are all absent from it. So the field
 * mounts the text box whenever the list is empty, AND offers a row inside the
 * menu that switches to it when the list is not -- otherwise a directory with
 * one session would take typing away entirely.
 *
 * The control swaps on the SETTLED state, never on `loading()`. A dialog opens
 * with no worker selected, so the first paint has nothing to fetch and an
 * un-settled field: it shows the menu, disabled, rather than a text box it
 * would replace a frame later while the user types into it. Pressing refresh
 * likewise keeps whichever control is mounted until an answer arrives.
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

  const { sessions, loading, settled, refresh } = useResumableSessions(source)

  // Set when the user asks for the text box although the menu has options.
  const [typing, setTyping] = createSignal(false)

  // A handle belongs to ONE worker, directory and provider. Nothing else clears
  // it, so without this a handle picked for `/repo-a` survives a change to
  // `/repo-b`: the menu finds no matching option and shows the raw handle as
  // though it were selected, Create stays enabled because the handle is
  // syntactically valid, and the worker is asked to resume another directory's
  // conversation. `defer` keeps a handle a caller seeded before the first run.
  createEffect(on(source, () => {
    props.state.setValue('')
    setTyping(false)
  }, { defer: true }))

  // The menu holds the field until a fetch for the CURRENT source has finished
  // and returned nothing. `loading()` alone read "nobody has asked yet" as
  // "empty", so the text input painted on the first frame of every dialog and
  // the menu replaced it once the effect ran -- under a user who may already be
  // typing. `settled()` distinguishes the two.
  const showMenu = () => !typing() && (!settled() || sessions().length > 0)

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
          onChange={(value) => {
            if (value === TYPE_A_HANDLE_VALUE) {
              // Switching to the text box must not carry the picked handle
              // into it: the user asked for a handle the list does not hold.
              props.state.setValue('')
              setTyping(true)
              return
            }
            props.state.setValue(value)
          }}
          sessions={sessions()}
          loading={loading()}
          invalid={props.state.error() !== null}
        />
      </Show>
    </LabeledField>
  )
}
