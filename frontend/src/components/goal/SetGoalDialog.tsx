import type { Component } from 'solid-js'
import { createSignal, createUniqueId, Show } from 'solid-js'
import { MarkdownEditor } from '~/components/chat/markdownEditor/MarkdownEditor'
import { formatNumber } from '~/components/chat/rendererUtils'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { Dialog } from '~/components/common/Dialog'
import { GOAL_OBJECTIVE_BYTE_LIMIT } from '~/generated/contracts/validate'
import { utf8ByteLength } from '~/lib/validate'
import { errorText } from '~/styles/shared.css'
import * as styles from './SetGoalDialog.css'

export interface SetGoalDialogProps {
  /**
   * The objective to start from. Set when REPLACING, so the reader edits what
   * is there instead of retyping it, and empty when setting a first goal.
   */
  initialObjective?: string
  onSubmit: (objective: string) => void
  onClose: () => void
}

/**
 * The share of the byte cap at which the remaining budget appears.
 *
 * A counter over a two-line objective states a number nobody is close to
 * spending; a counter over a page of prose is the warning that the refusal is
 * near. 90% is where the second becomes true.
 */
const BUDGET_NOTICE_RATIO = 0.9

/**
 * The editor's opening height and its ceiling, in pixels.
 *
 * A floor and a ceiling, so the box opens big enough to write a paragraph in
 * and then grows with the objective until it scrolls. `minHeight` rather than
 * `pinnedHeight`: a pinned height stops being a floor the moment the content
 * passes it, which would hold the box at 120px and leave the ceiling unused.
 */
const EDITOR_MIN_HEIGHT_PX = 120
const EDITOR_MAX_HEIGHT_PX = 320

/**
 * The editor for a session goal's objective.
 *
 * A dialog gives prose enough space. The Goals & To-dos popover has a 360px
 * width cap, and a paragraph editor does not fit there. The popover also stays
 * open on an inside click, while this form must close after submission.
 *
 * The field is the app's own markdown editor, so a goal is written with the
 * lists, links and code spans the card renders -- and with the keys the user
 * already learned in the composer.
 */
export const SetGoalDialog: Component<SetGoalDialogProps> = (props) => {
  /**
   * The editor's text, as the EDITOR reports it. Never seeded from the prop.
   *
   * A MIRROR, and only ever read for the byte NOTICE. It lags the document by
   * the 200ms debounce on Milkdown's `markdownUpdated` listener, so nothing
   * that decides an action may read it -- an advisory counter that is a fifth
   * of a second late is fine, and a button that refuses a click for a fifth of
   * a second is not. The seed arrives without that lag, because a programmatic
   * `set` reports the document as it applies it.
   */
  const [objective, setObjective] = createSignal('')
  /**
   * Why the last send was refused, or `undefined` when none was.
   *
   * The mirrored `objective` lags the document by the listener's debounce, so
   * it can never decide an ACTION -- only the send reads the live text. The
   * button therefore always clicks, and a refusal states its reason here rather
   * than being swallowed.
   */
  const [refusal, setRefusal] = createSignal<string | undefined>()
  /**
   * Whether the editor finished building and installed its imperative send.
   *
   * The ONE thing that legitimately disables the button. A click before this is
   * true reaches an `undefined` send and does nothing at all -- the same silent
   * no-op the debounced text used to cause, from the other end of the build. It
   * is a fact about the EDITOR, not about the text, so it cannot lag the
   * document.
   */
  const [ready, setReady] = createSignal(false)
  // The caption that names the editor -- see the `aria-labelledby` below.
  const hintId = createUniqueId()
  let triggerSend: (() => void | Promise<void>) | undefined

  const trimmed = () => objective().trim()
  // Measured in UTF-8 bytes, the unit the worker's cap counts in. The worker
  // REFUSES an objective over the limit rather than truncating it, so the
  // dialog has to refuse it too or the user meets an error they cannot see the
  // cause of.
  const usedBytes = () => utf8ByteLength(trimmed())
  const showsBudget = () => usedBytes() >= GOAL_OBJECTIVE_BYTE_LIMIT * BUDGET_NOTICE_RATIO

  /**
   * The ONE statement of the byte cap, for the notice AND for the refusal.
   *
   * Written once so a change to the cap cannot leave a warning that disagrees
   * with what the send accepts.
   */
  const overLimitMessage = (text: string): string | undefined => {
    const used = utf8ByteLength(text)
    if (used <= GOAL_OBJECTIVE_BYTE_LIMIT)
      return undefined
    return `Too long by ${formatNumber(used - GOAL_OBJECTIVE_BYTE_LIMIT)} bytes. `
      + `The limit is ${formatNumber(GOAL_OBJECTIVE_BYTE_LIMIT)}.`
  }

  /** Why the dialog refuses `text`, or `undefined` when it accepts it. */
  const refusalFor = (text: string): string | undefined =>
    text === '' ? 'Write the condition the agent works toward.' : overLimitMessage(text)

  /**
   * What the notice under the editor says.
   *
   * The over-limit half is proactive, from the mirrored text, so the warning
   * arrives while the user types rather than only when they click. The empty
   * half is not: an empty editor the user has not typed in yet owes no
   * complaint, so `refusal()` supplies that one, and only after a click.
   */
  const notice = () => refusal() ?? overLimitMessage(trimmed())

  /**
   * The ONE submit path.
   *
   * The editor's own send is what commits, whether the user pressed the key or
   * the footer button, which calls the same send. There is deliberately no
   * `type="submit"` button in this dialog: `Dialog` answers a bare Enter by
   * clicking the first one it finds, and that plus the editor's own Enter
   * handling would be two routes to one action. With none, Enter belongs to the
   * editor, and the user's own `enterKeyMode` preference governs it exactly as
   * it governs the composer.
   *
   * It decides on the text the EDITOR hands it, which is the live document.
   * Deciding on the mirrored signal instead left a window -- one debounce wide
   * -- where the button disagreed with the document, and a click inside it did
   * nothing at all, with no message.
   *
   * Returns `false` when it refuses, which is how the editor knows to keep what
   * the user wrote.
   */
  const submit = (markdown: string): boolean | void => {
    const text = markdown.trim()
    const why = refusalFor(text)
    if (why !== undefined) {
      setRefusal(why)
      return false
    }
    props.onSubmit(text)
    props.onClose()
  }

  return (
    <Dialog title="Session goal" onClose={props.onClose} data-testid="set-goal-dialog">
      {/* `<section>` and `<footer>` as DIRECT children of Dialog's `.body`. The
          stylesheet carries that bare shape: `> .body > section` gives the
          scroller and the edge bleed, `> .body > footer` the spacing.

          No `<form>` around them, because nothing here submits one -- see
          `submit`. An empty form is not inert: a form with no `onSubmit`
          NAVIGATES the page on implicit submission, so the first `<input>` a
          later change adds would reload the app. It would also nest inside the
          editor's own LinkPopover form, which is mounted whether that popover
          is open or not. */}
      <section class={styles.field}>
        {/* A plain `<div>`, not a `<label>`. The editor is a contenteditable
              region rather than a form control, so a `for` would point at no
              control and a click on the label would focus nothing.
              `aria-labelledby` carries the connection instead: the one
              instruction this dialog exists to give has to reach a screen
              reader, and without it the caret lands in an unnamed region. */}
        <div class={styles.label} id={hintId}>
          The agent keeps working until this condition holds.
        </div>
        <MarkdownEditor
          surface="goal"
          ariaLabelledBy={hintId}
          // The objective as DATA, seeded at BUILD time, so the editor never
          // holds an empty document that a later replace overwrites.
          //
          // And no `draftKey`: a persisted draft would resurrect prose the user
          // abandoned the next time they open Replace, in place of the
          // objective the worker actually holds -- and the objective, not the
          // draft, is what Replace exists to edit.
          initialMarkdown={props.initialObjective}
          onSend={submit}
          // The send must REACH `submit` even for an empty document, or an
          // empty send is a second silent no-op beside the one the button
          // used to be. `submit` is what states the reason.
          allowEmptySend
          onMarkdownChange={(markdown) => {
            setObjective(markdown)
            // The reader answered the complaint; take it down.
            setRefusal(undefined)
          }}
          minHeight={EDITOR_MIN_HEIGHT_PX}
          maxHeight={EDITOR_MAX_HEIGHT_PX}
          placeholder="Describe the condition the agent works toward..."
          imperative={{
            sendRef: (send) => { triggerSend = send },
            // The build is asynchronous, and this is the moment the editor
            // holds both a document and its imperative send. The button waits
            // for it, because a click before then reaches nothing.
            onReady: () => setReady(true),
          }}
        />
        <Show
          when={notice()}
          fallback={(
            <Show when={showsBudget()}>
              <span class={styles.label} data-testid="set-goal-budget">
                {`${formatNumber(GOAL_OBJECTIVE_BYTE_LIMIT - usedBytes())} bytes left`}
              </span>
            </Show>
          )}
        >
          {/* `role="alert"`, because a refusal that only a sighted reader
                notices is the silent no-op with extra steps. */}
          {why => (
            <span class={errorText} role="alert" data-testid="set-goal-too-long">
              {why()}
            </span>
          )}
        </Show>
      </section>
      <footer class={actionsFooter}>
        <button type="button" class="outline" onClick={() => props.onClose()}>Cancel</button>
        {/* Disabled ONLY while the editor is still building, which is the one
              state where a click genuinely cannot work. Never from the text:
              that reading is the debounced mirror, and a button disabled from
              it refuses a click the document would have accepted -- silently.
              Once the editor is up the button always clicks, and `submit`
              states why when it declines. */}
        <button
          type="button"
          data-testid="set-goal-submit"
          disabled={!ready()}
          onClick={() => void triggerSend?.()}
        >
          Set goal
        </button>
      </footer>
    </Dialog>
  )
}
