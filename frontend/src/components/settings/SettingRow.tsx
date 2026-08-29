import type { Component } from 'solid-js'
import type { SettingBinding, SettingControl, SettingDescriptor } from './types'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import { createMemo, createSignal, Show } from 'solid-js'
import { actionsFooter } from '~/components/common/actionsFooter.css'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { createRevealed } from '~/hooks/createRevealed'
import { createKeyedSeq } from '~/lib/keyedSeq'
import { errorText } from '~/styles/shared.css'
import { CUSTOM_EDITORS } from './controls/customEditors'
import { EnumControl } from './controls/EnumControl'
import { NumberControl } from './controls/NumberControl'
import { SecretControl } from './controls/SecretControl'
import { SliderControl } from './controls/SliderControl'
import { StringListControl } from './controls/StringListControl'
import { TextControl } from './controls/TextControl'
import { ToggleControl } from './controls/ToggleControl'
import * as styles from './SettingRow.css'

export interface SettingRowProps {
  descriptor: SettingDescriptor
  binding: SettingBinding
  /**
   * Inline write error from the owning store/user settings surface, when the
   * row's provider records one (the preferences context surfaces its own via
   * `lastSettingError`).
   */
  error?: string | null
}

/**
 * The enforced value in the VOCABULARY OF ITS OWN CONTROL, for the
 * "currently in effect" note.
 *
 * The note reports the same kind of fact that the control beside it
 * shows, so it must use the same words. A switch has two states, on and
 * off; the JSON literal `true` beneath a switch that reads OFF makes the
 * reader translate. An enum shows the option LABEL and never the wire
 * value, so `turnstile` beneath a pill that reads "Cloudflare Turnstile"
 * puts two names for one provider on one row.
 *
 * A number carries the unit that its control shows beside it, in the form
 * that control uses. NumberControl renders the unit as a separate label in
 * a flex row with a gap, so the note spaces it ("604800 seconds").
 * SliderControl concatenates the unit onto its own readout, so the note
 * does the same ("40%"). The two forms agree with what each unit needs: a
 * slider carries `%` alone, which attaches to its figure, and every unit a
 * number carries is a word, which stands apart.
 *
 * Every other kind prints the value itself: a string and a list read the
 * same in the note as in the control.
 */
export function formatEffectiveValue(control: SettingControl, value: unknown): string {
  switch (control.kind) {
    case 'toggle':
      // ToggleControl checks on `=== true` alone, so every other value
      // reads OFF in the control and must read Off here.
      return value === true ? 'On' : 'Off'
    case 'enum': {
      // An unmatched value keeps its raw text. The hub enforces a value
      // this client has no label for, and an empty note would report
      // nothing at all.
      const raw = String(value)
      return control.options.find(o => o.value === raw)?.label ?? raw
    }
    // Both numeric cases test the UNIT and never the value, so an enforced
    // 0 keeps its unit: a queue budget of 0 auto-sizes, and the byte count
    // the hub settled on is the whole point of the note. Most number keys
    // declare no unit, and those must gain no separator.
    case 'number':
      return control.unit ? `${String(value)} ${control.unit}` : String(value)
    case 'slider':
      return control.unit ? `${String(value)}${control.unit}` : String(value)
    default:
      return String(value)
  }
}

/**
 * One setting: label, help, control, and the status area (Customized + reset
 * for customized rows, a Requires Restart badge for restart-class descriptors, the
 * inline error, and the "currently in effect" note when the hub enforces
 * something other than the configured value).
 *
 * Dual rows carry the scope chip — a radio menu between "Use account default"
 * and "Override on this device"; single-tier rows render the plain
 * "This device" / "Account" note instead.
 */
export const SettingRow: Component<SettingRowProps> = (props) => {
  const d = () => props.descriptor
  const [menuOpen, setMenuOpen] = createSignal(false)
  // A failed write through this row, rendered inline. The owning surface
  // records its own copy (the context's lastSettingError or the store's
  // writeError); this is the row-local view so a failure is never silent.
  const [writeError, setWriteError] = createSignal<string | null>(null)
  let chipRef: HTMLButtonElement | undefined

  /** Defers the `custom` editor below; see the `custom` case of `control`. */
  const reveal = createRevealed()

  // The row shows TWO facts, and the split is deliberate. The CONTROL
  // carries the configured value, because the control is what the admin
  // edits and an edit changes exactly that value. This note carries what
  // the hub enforces right now, because a read-time rule (dev mode holding
  // sign-up open, a queue budget of 0 auto-sizing) applies for the life of
  // the process without storing anything — a fact to report, not a value
  // to edit. Binding both halves to the enforced value made this note
  // print the control's own value straight back.
  //
  // Memoized: `binding.effective` parses both JSON documents and compares
  // them, and the status row reads it twice — once to decide whether to
  // render the note and once to print it.
  //
  // The `<Show>` below must stay a plain boolean guard rather than the
  // callback form: an enforced `false` or `0` is a real value, and the
  // callback form treats falsy as absent. `undefined` is the ONE absent
  // value, which is why this memo maps every present value to its text
  // and passes `undefined` through untouched.
  const effectiveText = createMemo<string | undefined>(() => {
    const read = props.binding.effective
    const value = read ? read() : undefined
    return value === undefined ? undefined : formatEffectiveValue(d().control, value)
  })

  /**
   * The newest write this row issued.
   *
   * This row must not report a superseded write's rejection. Both write paths
   * beneath already skip their own bookkeeping for one — the store leaves
   * its `writeError` alone, and the account path leaves the value alone —
   * but both still reject, so without this guard the row would show the
   * value the LATER write stored beside the reason the EARLIER one failed,
   * permanently. The same helper holds the guard over the store's writes
   * and over the account tier's.
   *
   * UNKEYED: one row edits one setting, so its set and its reset share the
   * single counter and either supersedes the other.
   */
  const writeSeq = createKeyedSeq()
  const reportFailure = (seq: number, err: unknown) => {
    if (!writeSeq.isNewest(undefined, seq))
      return
    setWriteError(err instanceof Error ? err.message : String(err))
  }

  /**
   * Write `v` through the binding, and REPORT the outcome: true when the
   * binding accepted it, false when it refused.
   *
   * The promise never rejects. A control that holds the typed text in the
   * DOM (the text input) must put the stored value back
   * when a write is refused, and it can only know to do that from the
   * return value — Solid assigns `value` only when the tracked expression
   * CHANGES, and a refused write leaves `props.value` exactly as it was.
   * Callers that have nothing to repair discard the promise.
   */
  const commit = (v: unknown): Promise<boolean> => {
    const seq = writeSeq.next()
    setWriteError(null)
    return Promise.resolve(props.binding.set(v))
      .then(() => true)
      .catch((err: unknown) => {
        reportFailure(seq, err)
        return false
      })
  }

  const runReset = () => {
    const seq = writeSeq.next()
    setWriteError(null)
    void Promise.resolve(props.binding.reset?.()).catch((err: unknown) => reportFailure(seq, err))
  }

  const control = () => {
    const c = d().control
    switch (c.kind) {
      case 'enum':
        return (
          <EnumControl
            ariaLabel={d().label}
            options={c.options}
            value={String(props.binding.value() ?? '')}
            onChange={commit}
          />
        )
      case 'toggle':
        return (
          <ToggleControl
            ariaLabel={d().label}
            value={props.binding.value() === true}
            onChange={commit}
          />
        )
      case 'slider':
        return (
          <SliderControl
            ariaLabel={d().label}
            min={c.min}
            max={c.max}
            step={c.step}
            unit={c.unit}
            value={typeof props.binding.value() === 'number' ? props.binding.value() as number : c.min}
            onChange={commit}
          />
        )
      case 'number':
        return (
          <NumberControl
            ariaLabel={d().label}
            min={c.min}
            max={c.max}
            step={c.step}
            unit={c.unit}
            value={typeof props.binding.value() === 'number' ? props.binding.value() as number : undefined}
            onChange={commit}
          />
        )
      case 'text':
        return (
          <TextControl
            ariaLabel={d().label}
            placeholder={c.placeholder}
            value={typeof props.binding.value() === 'string' ? props.binding.value() as string : undefined}
            onChange={commit}
          />
        )
      case 'secret':
        return (
          <SecretControl
            ariaLabel={d().label}
            isSet={c.isSet()}
            onSet={v => Promise.resolve(props.binding.set(v))}
          />
        )
      case 'stringList':
        return (
          <StringListControl
            ariaLabel={d().label}
            addLabel={c.addLabel}
            value={Array.isArray(props.binding.value()) ? props.binding.value() as string[] : []}
            onChange={commit}
          />
        )
      case 'custom': {
        const Editor = CUSTOM_EDITORS[c.id]
        if (!Editor)
          return null
        // DEFERRED until the row comes into view. A custom editor is a whole
        // panel of its own, and several load on mount: AccountPasskeys issues
        // ListPasskeys and AccountConnectedApps runs a keyset loop over
        // ListMyAPITokens. The Account group leads the navigation, so every
        // Ctrl+, mounted both -- two list requests for a user who came for
        // Appearance and clicks away -- and both of those rows sit below the
        // visible area.
        return (
          <Show when={reveal.revealed()}>
            <Editor />
          </Show>
        )
      }
      case 'action':
        return (
          // Right-aligned like every section action in the dialog: an action
          // row is the closing line of its editor, and Oat's own dialog
          // footers put their primaries there too.
          <div class={actionsFooter}>
            <ConfirmButton
              /*
                A danger action is the FILLED danger ConfirmButton, the same
                chrome "Remove all" on the trusted-worker-keys panel wears: it
                ends a whole feature in one request with no dialog of its own,
                so the button itself is the confirmation and carries the weight.
                A small outline here rendered a second danger idiom beside that
                one, quieter than the ending it performs.
              */
              data-variant={c.danger ? 'danger' : undefined}
              data-testid={`setting-action-${d().id}`}
              onClick={() => void commit(true)}
            >
              {c.label}
            </ConfirmButton>
          </div>
        )
    }
  }

  const scopeNote = () => {
    const scope = d().scope
    if (scope === 'dual') {
      const overridden = () => props.binding.overridden?.() === true
      return (
        <>
          <button
            ref={chipRef}
            type="button"
            class={styles.scopeChip}
            aria-haspopup="menu"
            aria-expanded={menuOpen()}
            data-testid={`scope-chip-${d().id}`}
            onClick={() => setMenuOpen(o => !o)}
          >
            {overridden() ? 'This device' : 'Account default'}
            <Icon icon={ChevronDown} size="xs" aria-hidden="true" />
          </button>
          <DropdownMenu
            open={menuOpen}
            onToggle={setMenuOpen}
            anchorRef={() => chipRef}
            aria-label={`${d().label} scope`}
          >
            <DropdownMenuCheckableItem
              kind="radio"
              label="Use account default"
              checked={!overridden()}
              onSelect={() => {
                props.binding.clearOverride?.()
                setMenuOpen(false)
              }}
            />
            <DropdownMenuCheckableItem
              kind="radio"
              label="Override on this device"
              checked={overridden()}
              onSelect={() => {
                props.binding.beginOverride?.()
                setMenuOpen(false)
              }}
            />
          </DropdownMenu>
        </>
      )
    }
    const note = scope === 'account' ? 'Account' : scope === 'hub' ? 'Hub' : 'This device'
    return <span class={styles.scopePlain}>{note}</span>
  }

  return (
    <div
      class={styles.row}
      data-setting-id={d().id}
      ref={(el) => {
        // Only a `custom` row defers anything, so only a `custom` row pays for
        // an observer. Every other control is already in the markup.
        if (d().control.kind === 'custom')
          reveal.observe(el)
      }}
    >
      {/*
        An H3, the Oat heading element a labelled block uses: the row is the
        panel's one unit of structure, and a heading gives it a place in the
        document outline a span cannot. The TYPE is Oat's own h3 rule,
        deliberately -- the compact span size does not come back (the label
        style's comment states the same rule). The zeroed margin alone is
        stated here, because a settings row must not take a page heading's
        spacing.
      */}
      <div class={styles.headerRow}>
        <h3 class={styles.label}>{d().label}</h3>
        {scopeNote()}
      </div>
      <Show when={d().help}>
        {help => <div class={styles.helpText}>{help()}</div>}
      </Show>
      {control()}
      <div class={styles.statusRow}>
        <Show when={d().restart}>
          <span class="badge" data-variant="warning">Requires Restart</span>
        </Show>
        <Show when={props.binding.customized?.()}>
          <span>Customized</span>
          <Show when={props.binding.reset}>
            <Show
              when={props.binding.resetsWholeKey}
              fallback={(
                <button
                  type="button"
                  class={styles.resetButton}
                  data-testid={`setting-reset-${d().id}`}
                  onClick={runReset}
                >
                  Reset
                </button>
              )}
            >
              {/*
                A reset that takes MORE than this row's value with it asks
                first, and says what goes. The reset RPC removes the key's
                whole stored row, so an unconfirmed "Reset" on the SMTP
                host also destroyed the encrypted password — which the UI
                can never redisplay.
              */}
              {key => (
                <ConfirmButton
                  class={styles.resetButton}
                  data-variant="danger"
                  data-testid={`setting-reset-${d().id}`}
                  onClick={runReset}
                >
                  {`Reset all of ${key()}`}
                </ConfirmButton>
              )}
            </Show>
          </Show>
        </Show>
        <Show when={props.error ?? writeError()}>
          <span class={errorText} data-testid={`setting-error-${d().id}`}>{props.error ?? writeError()}</span>
        </Show>
        <Show when={effectiveText() !== undefined}>
          <span class={styles.effectiveNote}>
            Currently in effect:
            {' '}
            {effectiveText()}
          </span>
        </Show>
      </div>
    </div>
  )
}
