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
  // Playwright fixture parameters (e.g. `authenticatedWorkspace`) must be destructured
  // to activate the fixture, even when not directly referenced in the test body.
  files: ['tests/e2e/**/*.spec.ts'],
  rules: {
    'unused-imports/no-unused-vars': ['error', {
      argsIgnorePattern: '^(authenticatedWorkspace|workspace|leapmuxServer|separateHubWorker)$',
    }],
  },
})
