import type { JSX } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { clippedText } from '~/styles/shared.css'
import { detailLine } from './ClippedText.css'

export interface ClippedTextProps {
  /** The one-line label. */
  text: string
  /**
   * An explanation that the label cannot carry: a description the row has no
   * room for, or the meaning of a terse status.
   *
   * The tooltip then shows ALWAYS, because an explanation is not recoverable in
   * any other way, and it renders the label ABOVE the explanation. The
   * explanation adds to the label; it never replaces it. A label that clips
   * therefore keeps its route back even when it also carries an explanation.
   *
   * An empty string counts as absent.
   */
  detail?: string
  /**
   * Overrides when the tooltip appears. The default is `'clipped'`, or
   * `'always'` when {@link ClippedTextProps.detail} is set. Pass this only to
   * override that rule.
   */
  showWhen?: 'always' | 'clipped'
  /** Extra classes for the span: the font face, the colour, the flex sizing. */
  class?: string
  /** `data-testid` for the span, so a test can select the label itself. */
  testId?: string
}

/**
 * A label held to ONE line, clipped with an ellipsis, with the full text on
 * hover once it is actually clipped.
 *
 * The two halves belong together: an ellipsis hides text, and the tooltip is
 * the only way the reader gets it back. Keeping them in one component is what
 * makes "clipped with no way to read the rest" impossible to write by accident.
 * `detail` follows the same rule: it is added ABOVE its explanation rather than
 * put in its place, so a caller cannot spend the label's own route back.
 *
 * This component is the SOLE owner of the clipping rule. A caller passes
 * decoration only -- the font face, the colour, the flex sizing -- and must not
 * compose `clippedText` into that class, because a second owner makes the
 * removal of either one invisible.
 *
 * One authorized override exists: `todoListWrapping` in
 * `~/components/todo/TodoList.css.ts` turns the clip back off for the chat
 * transcript, where the card is wide enough to show the whole label. It wins on
 * specificity, not on stylesheet order.
 *
 * Renders exactly one element, because `Tooltip` resolves its target as the
 * single direct element child of its wrapper and disables itself otherwise.
 */
export function ClippedText(props: ClippedTextProps) {
  const spanClass = () => (props.class ? `${clippedText} ${props.class}` : clippedText)
  // An EMPTY explanation is no explanation. It must not force the tooltip open
  // on a label that fits, and it must not render a blank second line.
  const detail = () => props.detail || undefined
  const content = (): JSX.Element | undefined => {
    const explanation = detail()
    if (!explanation)
      return undefined
    return (
      <>
        <div>{props.text}</div>
        <div class={detailLine}>{explanation}</div>
      </>
    )
  }
  return (
    <Tooltip
      text={props.text}
      content={content()}
      showWhen={props.showWhen ?? (detail() ? 'always' : 'clipped')}
    >
      <span class={spanClass()} data-testid={props.testId}>
        {props.text}
      </span>
    </Tooltip>
  )
}
