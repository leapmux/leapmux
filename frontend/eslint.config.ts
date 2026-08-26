import antfu from '@antfu/eslint-config'

export default antfu({
  stylistic: {
    indent: 2,
    quotes: 'single',
  },
  solid: true,
  ignores: ['src/gen/**', '.vinxi/**', '.output/**', 'app.config.timestamp_*'],
}, {
  // Treat `useDialogSubmit`'s returned helpers as reactive entry points.
  // `run` and `formHandler` invoke their callback synchronously inside an
  // event-handler call stack, so a body that captures reactive props is
  // safe — the captures are read before any subsequent prop update. The
  // plugin already auto-detects `create*` / `use*` names; this option
  // extends that allowlist to the helpers returned from useDialogSubmit.
  //
  // The `files` filter mirrors antfu's solid config (JSX/TSX only) so we
  // don't widen the rule's surface area to `.ts` files where it wasn't
  // previously running.
  files: ['**/*.jsx', '**/*.tsx'],
  rules: {
    'solid/reactivity': ['warn', {
      customReactiveFunctions: ['run', 'formHandler'],
    }],
  },
}, {
  // Every browser-storage access goes through `~/lib/browserStorage`, which
  // composes the account-scoped key and the `{ v, e }` TTL envelope. A direct
  // call skips both: a second account on the browser can read the value, and
  // the next page-load sweep deletes it, so the feature works once and then
  // silently forgets its state on every reload.
  //
  // At the AST level rather than by text, because the class is "a reference to
  // the global", not one spelling of it: `localStorage['k'] = v` and
  // `const s = sessionStorage` write the same broken entry.
  // `src/test-support/storageKeysAreRegistered.test.ts` is the backstop, and it
  // also holds the two registry rules that have no lint equivalent.
  //
  // Tests and E2E specs are exempt. A unit test drives the gateway's own
  // behaviour, and an E2E `page.evaluate` body runs in the browser, where the
  // module does not exist.
  files: ['src/**/*.ts', 'src/**/*.tsx'],
  ignores: ['src/lib/browserStorage.ts', 'src/**/*.test.ts', 'src/**/*.test.tsx'],
  rules: {
    'no-restricted-globals': ['error', { name: 'localStorage', message: 'Route browser storage through ~/lib/browserStorage.' }, { name: 'sessionStorage', message: 'Route browser storage through ~/lib/browserStorage.' }],
    'no-restricted-properties': ['error', { object: 'window', property: 'localStorage', message: 'Route browser storage through ~/lib/browserStorage.' }, { object: 'window', property: 'sessionStorage', message: 'Route browser storage through ~/lib/browserStorage.' }, { object: 'globalThis', property: 'localStorage', message: 'Route browser storage through ~/lib/browserStorage.' }, { object: 'globalThis', property: 'sessionStorage', message: 'Route browser storage through ~/lib/browserStorage.' }],
  },
}, {
  // `title` on a DOM element is banned. Use `<Tooltip>` (or a component that
  // routes its own `title` prop through one, as `IconButton` does).
  //
  // Two reasons, and the second is the one that bites silently. A native
  // `title` renders the OS tooltip, which ignores the app's theme and
  // typography, waits a browser-controlled delay, and never appears on touch.
  // And on a control with no `aria-label`, a `title` long enough to state a
  // reason BECOMES the accessible name: a screen reader then announces three
  // sentences of remedy where "Add passkey" belongs, and every by-name lookup
  // stops matching. Nothing in the type system catches it, and it renders
  // fine, so it survives review.
  //
  // The carve-out this replaced was "a DISABLED control may use `title`,
  // because it takes no pointer events and `<Tooltip>` cannot fire on it".
  // `<Tooltip>` covers that case now: it gives its wrapper a box, listens
  // there, and leaves an offscreen description in `aria-describedby` for as
  // long as the control is disabled.
  //
  // A LOWERCASE element name only. `title` on a component is that component's
  // own prop -- `<Dialog title>` is a heading, `<IconButton title>` is a
  // tooltip -- and the selector cannot know which. A component that SPREADS
  // its props onto a DOM node closes that hole in the type system instead, by
  // omitting `title` from its prop type; `IconButton` and `ConfirmButton` both
  // do.
  files: ['src/**/*.ts', 'src/**/*.tsx', 'tests/**/*.ts', 'tests/**/*.tsx'],
  rules: {
    'no-restricted-syntax': ['error', {
      selector: 'JSXOpeningElement[name.type="JSXIdentifier"][name.name=/^[a-z]/] > JSXAttribute[name.name="title"]',
      message: 'Do not put `title` on a DOM element: it renders the unthemed OS tooltip, and it silently becomes the element\'s accessible name. Wrap the element in <Tooltip text={...}> instead -- it works on a disabled control too.',
    }],
  },
}, {
  // Playwright fixture parameters (e.g. `authenticatedWorkspace`) must be destructured
  // to activate the fixture, even when not directly referenced in the test body.
  files: ['tests/e2e/**/*.spec.ts'],
  rules: {
    'unused-imports/no-unused-vars': ['error', {
      argsIgnorePattern: '^(authenticatedWorkspace|workspace|leapmuxServer|separateHubWorker)$',
    }],
  },
})
