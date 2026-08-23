import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// Modal <dialog> lives in the top layer, so it escapes body's
// `padding-top: env(safe-area-inset-top)`. The panel must re-declare the
// safe-area insets itself; without that, the close button sits under the
// status bar / Dynamic Island in an iOS standalone PWA and is untappable.
//
// jsdom cannot resolve env(safe-area-inset-*) and Playwright's Chromium
// reports 0 on desktop, so this is a source contract: the declarations
// that fix the bug must stay in Dialog.css.ts. Geometry with non-zero
// insets is a real-device concern (see the comment on the :modal rule).

const source = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), 'Dialog.css.ts'),
  'utf8',
)

describe('dialog safe-area insets (Dialog.css.ts)', () => {
  it('pins the modal panel inside every safe-area inset', () => {
    for (const inset of [
      'safe-area-inset-top',
      'safe-area-inset-right',
      'safe-area-inset-bottom',
      'safe-area-inset-left',
    ]) {
      expect(source, inset).toContain(`env(${inset}, 0px)`)
    }
  })

  it('applies the insets on the :modal selector that beats the UA inset:0', () => {
    // `dialog.${standard}:modal` is what outranks `dialog:modal { inset: 0 }`.
    // A rule on `.standard` alone loses that fight and the panel stays
    // full-bleed under the status bar.
    expect(source).toMatch(/dialog\.\$\{standard\}:modal/)
  })

  it('keeps short dialogs content-sized inside the safe rectangle', () => {
    // top+bottom without fit-content stretches a confirm dialog to fill
    // the safe area (abspos height:auto stretch).
    expect(source).toMatch(/['"]?height['"]?\s*:\s*['"]fit-content['"]/)
  })

  it('lets tall and huge dialogs fill the safe rectangle on the phone band', () => {
    expect(source).toMatch(/dialog\.\$\{standard\}\.\$\{tall\}:modal/)
    expect(source).toMatch(/dialog\.\$\{standard\}\.\$\{huge\}:modal/)
    expect(source).toContain('SAFE_MAX_HEIGHT')
  })
})
