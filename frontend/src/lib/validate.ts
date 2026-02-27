// eslint-disable-next-line no-control-regex
const NAME_FORBIDDEN_G = /[\x00-\x1F\x7F"\\]/g

/**
 * Sanitizes and validates a name/title string.
 * Forbidden characters (control characters, " and \) are silently stripped.
 * Returns the sanitized string and an error if the result is empty or exceeds 128 characters.
 */
export function sanitizeName(name: string): { value: string, error: string | null } {
  const value = name.replace(NAME_FORBIDDEN_G, '').trim()
  let error: string | null = null
  if (value === '') {
    error = 'Name must not be empty'
  }
  else if (value.length > 128) {
    error = 'Name must be at most 128 characters'
  }
  return { value, error }
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

const SLUG_PATTERN = /^[a-z0-9-]+$/

/**
 * Sanitizes and validates a GitHub-style slug (username or organization name).
 * Trims whitespace and lowercases, then validates.
 * Rules: 1-32 chars, lowercase alphanumeric and hyphens only,
 * no leading/trailing hyphens, no consecutive hyphens.
 * Returns [cleanedSlug, null] on success, or ['', errorMessage] on failure.
 */
export function sanitizeSlug(fieldName: string, value: string): [string, string | null] {
  const slug = value.trim().toLowerCase()
  if (slug === '') {
    return ['', `${fieldName} must not be empty`]
  }
  if (slug.length > 32) {
    return ['', `${fieldName} must be at most 32 characters`]
  }
  if (!SLUG_PATTERN.test(slug)) {
    return ['', `${fieldName} must contain only letters, numbers, and hyphens`]
  }
  if (slug.startsWith('-')) {
    return ['', `${fieldName} must not start with a hyphen`]
  }
  if (slug.endsWith('-')) {
    return ['', `${fieldName} must not end with a hyphen`]
  }
  if (slug.includes('--')) {
    return ['', `${fieldName} must not contain consecutive hyphens`]
  }
  return [slug, null]
}
