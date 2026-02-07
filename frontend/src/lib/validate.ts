const NAME_PATTERN = /^[\w .\-]+$/

/**
 * Validates a name/title string.
 * Rules: trimmed non-empty, max 64 chars, only [a-zA-Z0-9 _\-.].
 * Returns an error message string, or null if valid.
 */
export function validateName(name: string): string | null {
  const trimmed = name.trim()
  if (trimmed === '') {
    return 'Name must not be empty'
  }
  if (trimmed.length > 64) {
    return 'Name must be at most 64 characters'
  }
  if (!NAME_PATTERN.test(trimmed)) {
    return 'Name must contain only letters, numbers, spaces, hyphens, underscores, and dots'
  }
  return null
}

/**
 * Returns true if the name is valid.
 */
export function isValidName(name: string): boolean {
  return validateName(name) === null
}

// Characters forbidden in git branch names: space ~ ^ : ? * [ ] \
// Also control characters (0x00-0x1F, 0x7F).
// eslint-disable-next-line no-control-regex
const BRANCH_FORBIDDEN_CHARS = /[\x00-\x1F\x7F ~^:?*[\]\\]/

/**
 * Validates a git branch name according to git-check-ref-format rules.
 * Returns an error message string, or null if valid.
 */
export function validateBranchName(name: string): string | null {
  if (name === '') {
    return 'Branch name must not be empty'
  }
  if (name.length > 256) {
    return 'Branch name must be at most 256 characters'
  }
  if (BRANCH_FORBIDDEN_CHARS.test(name)) {
    return 'Branch name contains invalid characters'
  }
  if (name.startsWith('/') || name.startsWith('.') || name.startsWith('-') || name.startsWith('@')) {
    return 'Branch name must not start with /, ., -, or @'
  }
  if (name.endsWith('/') || name.endsWith('.') || name.endsWith('.lock')) {
    return 'Branch name must not end with /, ., or .lock'
  }
  if (name.includes('..')) {
    return 'Branch name must not contain ..'
  }
  if (name.includes('//')) {
    return 'Branch name must not contain //'
  }
  if (name.includes('/.')) {
    return 'Branch name must not contain /.'
  }
  return null
}

/**
 * Returns true if the branch name is valid.
 */
export function isValidBranchName(name: string): boolean {
  return validateBranchName(name) === null
}
