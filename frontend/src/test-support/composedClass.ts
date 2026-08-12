/**
 * A CSS selector that matches every class in a vanilla-extract style.
 *
 * A composed style (`style([base, {...}])`) exports SPACE-SEPARATED class
 * names, so `.${style}` is a DESCENDANT selector whose right side is a bare
 * type selector. It parses cleanly and matches nothing, so `querySelector`
 * returns `null` and an assertion that expects absence passes for the wrong
 * reason. This joins the names into one compound selector instead.
 *
 * Throws on an empty class name. `''.split(/\s+/)` gives `['']`, which builds
 * the selector `.` -- `querySelector` then raises a `SyntaxError` that names
 * neither this helper nor the caller.
 */
export function classSelector(className: string): string {
  const classes = className.trim().split(/\s+/).filter(Boolean)
  if (classes.length === 0)
    throw new Error('classSelector: the class name is empty')
  return classes.map(c => `.${c}`).join('')
}
