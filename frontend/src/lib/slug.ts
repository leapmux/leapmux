/**
 * A label reduced to something addressable: lowercase, and every run of
 * non-alphanumerics folded to one hyphen.
 *
 * The labels that reach this carry spaces, dots, slashes, colons, backslashes
 * and a middle dot, none of which belong in a selector. A Windows drive root
 * (`C:\`) reduces to its letter; a UNC root (`\\srv\share`) to `srv-share`.
 *
 * The fold is LOSSY: `worker · a/b` and `worker-a-b` both reduce to
 * `worker-a-b`, `foo_bar` and `foo-bar` both reduce to `foo-bar`, and a label
 * with no ASCII alphanumerics at all reduces to the empty string. A caller that
 * needs a UNIQUE id must resolve a collision itself -- see `targetSlugs` in
 * `~/components/workspace/RepositoryTargetMenu`.
 */
export function slugify(label: string): string {
  // No `i` flag: `.toLowerCase()` already ran, and on a NEGATED class the flag
  // would stop `[^a-z0-9]` from excluding uppercase letters.
  return label.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}
