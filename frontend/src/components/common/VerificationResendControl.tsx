import type { Component, JSX } from 'solid-js'
import type { useVerificationResend } from '~/lib/useVerificationResend'

import { Show } from 'solid-js'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { CaptchaSection } from '~/components/common/CaptchaSection'
import { CAPTCHA_ACTION } from '~/generated/contracts/captcha'
import { errorText, successText } from '~/styles/shared.css'

/** What useVerificationResend hands back; one object, passed whole. */
type VerificationResend = ReturnType<typeof useVerificationResend>

interface VerificationResendControlProps {
  /** The hook's value: captcha form, countdown, status, and the send. */
  resend: VerificationResend
  /** Runs before the send fires; a surface clears a sibling error here. */
  beforeResend?: () => void
  /** Extra content inside the button row (the "Enter the code" link). */
  footerExtra?: JSX.Element
}

/**
 * The resend-verification surface: error line, status line, captcha
 * widget, and the footer button, in that order. Both surfaces that offer
 * a resend (/verify-email and Preferences -> Account) render this, so the
 * widget, the cooldown contract, and the status texts cannot drift between
 * them -- the useVerificationResend doc's "neither surface can forget the
 * widget" becomes this component instead of each call site's memory.
 *
 * The widget arms only when a resend is LEGAL: the countdown at zero AND
 * the cooldown seed already answered (a countdown of 0 before the auth
 * bootstrap lands means "not known yet", not "legal now" -- arming on
 * that reading mints a challenge the arriving seed immediately unmounts).
 * Mounting it during a live cooldown mints a challenge that typically
 * expires unused -- the page navigates away on verify success -- and a
 * reCAPTCHA v3 field re-mints one every 110 seconds while visible. The
 * cost of waiting: the widget pops in when the countdown ends, and a
 * pre-solve during the cooldown is no longer possible. The button's own
 * blocksSubmit gate still covers the pop-in moment.
 *
 * The status line carries the success text (and the soft-failure text a
 * refused send leaves), so it takes the success styling both surfaces
 * showed before they shared this component.
 */
export const VerificationResendControl: Component<VerificationResendControlProps> = (props) => {
  // The hook result is a stable bag of functions and signals, not a reactive
  // value the component must track: reading props.resend once is the intent.
  // eslint-disable-next-line solid/reactivity -- stable hook bag
  const r = props.resend
  return (
    <>
      <Show when={r.error()}>
        {msg => <div class={errorText}>{msg()}</div>}
      </Show>
      <Show when={r.status()}>
        {msg => <div class={successText} data-testid="verify-email-resend-status">{msg()}</div>}
      </Show>
      <Show when={r.countdown() === 0 && r.cooldownKnown()}>
        <CaptchaSection action={CAPTCHA_ACTION.resendVerification} captcha={r.captcha} />
      </Show>
      <div class={actionsFooter}>
        <button
          type="button"
          data-testid="verify-email-resend"
          onClick={() => {
            props.beforeResend?.()
            void r.resend()
          }}
          disabled={r.disabled()}
        >
          {r.buttonLabel()}
        </button>
        {props.footerExtra}
      </div>
    </>
  )
}
