/**
 * Stop a press from moving focus to the control that the user pressed.
 *
 * Bind it to `mousedown`, which is the event whose default action moves focus
 * to a pressed control. The `click` still fires, so the control still acts.
 *
 * Every control that acts on a text field the user is typing in wants this,
 * and the composer's Send, Interrupt and control-request buttons are those
 * controls here. Without it the platforms disagree about one press: Safari
 * never focuses a pressed button, while Chrome and Firefox do on Windows,
 * Linux and Android. The composer then cannot tell "the user gave the caret
 * away before pressing Send" from "the press itself took the caret", and it
 * restores focus in both -- which raises the on-screen keyboard that the user
 * had just put away. See `decideSendFocus` in
 * `~/components/chat/markdownEditor/sendFocus.ts`, which reads the focus state
 * this preserves.
 */
export function keepFocusOnPress(event: MouseEvent): void {
  event.preventDefault()
}
