import { bundledLanguagesInfo } from 'shiki/langs'

/** Language option for the code-block language picker. */
export interface LangOption {
  id: string
  label: string
}

/**
 * All languages the editor can highlight, for the code-block language picker.
 *
 * Derived from Shiki's bundled grammar catalog (one entry per grammar; the editor
 * lazy-loads the grammar on demand via the Oniguruma highlighter), so the picker
 * always matches what the editor can actually render -- ~235 languages, up from the
 * hand-curated Rouge list. `plaintext` (no grammar) is listed first; the rest are
 * sorted by display label.
 */
export const LANGUAGES: LangOption[] = [
  { id: 'plaintext', label: 'Plain Text' },
  ...bundledLanguagesInfo
    .map(info => ({ id: info.id, label: info.name }))
    .sort((a, b) => a.label.localeCompare(b.label)),
]
