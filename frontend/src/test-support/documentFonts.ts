import { onTestFinished } from 'vitest'

/** The part of the `document.fonts` API that the code under test reads. */
export interface DocumentFontsStub {
  check?: (spec: string) => boolean
  load?: (spec: string) => Promise<unknown>
}

/**
 * Replace `document.fonts` with `api` for the running test. jsdom implements no font API,
 * so a test that reads one installs it here. Pass `undefined` to remove the property, as in
 * a document without a font API.
 *
 * The helper registers its restore with `onTestFinished`, so no test can forget it. The
 * restore defines the property that the document held before, or deletes the property when
 * the document held none. Vitest runs the callbacks of `onTestFinished` in reverse order,
 * so two calls in one test restore the original property last.
 */
export function stubDocumentFonts(api: DocumentFontsStub | undefined): void {
  const original = Object.getOwnPropertyDescriptor(document, 'fonts')
  onTestFinished(() => {
    if (original)
      Object.defineProperty(document, 'fonts', original)
    else
      Reflect.deleteProperty(document, 'fonts')
  })
  if (api === undefined)
    Reflect.deleteProperty(document, 'fonts')
  else
    Object.defineProperty(document, 'fonts', { configurable: true, value: api })
}
