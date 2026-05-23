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
  // Playwright fixture parameters (e.g. `authenticatedWorkspace`) must be destructured
  // to activate the fixture, even when not directly referenced in the test body.
  files: ['tests/e2e/**/*.spec.ts'],
  rules: {
    'unused-imports/no-unused-vars': ['error', {
      argsIgnorePattern: '^(authenticatedWorkspace|workspace|leapmuxServer|separateHubWorker)$',
    }],
  },
})
