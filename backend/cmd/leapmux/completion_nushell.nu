# Save as leapmux-completion.nu and load with use leapmux-completion.nu *.

# Nushell supplies the original spelling of each word, including unfinished
# quotes at the cursor. Decode literal strings as data before calling Cobra.
def leapmux-completion-word [word: string] {
    let first = ($word | %str substring 0..<1)
    if $first == "\u{60}" {
        let tail = ($word | %str substring 1..)
        return (if ($tail | %str ends-with $first) { $tail | %str substring ..<-1 } else { $tail })
    }
    let closer = if $first in ['"' "'"] {
        $first
    } else {
        let raw = ($word | %parse --regex "^r(?<marks>#+)'" | %first)
        if $raw == null { '' } else { "'" + $raw.marks }
    }
    if $closer == '' { return $word }
    try { $word | %from nuon } catch {
        try { ($word + $closer) | %from nuon } catch { $word }
    }
}

def leapmux-completion-text [value: string] {
    if $value =~ '^[[:alnum:]_.:/=,@%+-]+$' { $value } else { $value | %to nuon }
}

def leapmux-complete [place: record] {
    let args = ($place.command | %skip 1 | %each {|raw|
        let word = (leapmux-completion-word $raw)
        if $word =~ '^--[^=]+=' {
            let parts = ($word | %split row --number 2 '=')
            $parts.0 + '=' + (leapmux-completion-word $parts.1)
        } else { $word }
    })
    if ($args | %is-empty) { return [] }

    let response = (try { ^leapmux __complete ...$args | %complete } catch { null })
    if $response == null or $response.exit_code != 0 { return [] }
    let lines = ($response.stdout | %lines)
    let status = ($lines | %last)
    if $status == null or $status !~ '^:[0-9]{1,2}$' { return [] }
    let directive = ($status | %str substring 1.. | %into int)

    # A Cobra error produces no suggestions. LeapMux does not emit file
    # extension or directory filter directives. Those fields are filters,
    # so refuse them here instead of inserting them as arguments.
    if $directive > 63 or ($directive | %bits and 25) != 0 { return [] }

    let word = ($args | %last)
    let prefix = if $word =~ '^--[^=]+=' { ($word | %split row --number 2 '=' | %first) + '=' } else { '' }
    let entries = ($lines | %drop)
    if ($entries | %is-empty) {
        if ($directive | %bits and 4) != 0 { return [] }
        if $prefix == '' { return null }
        # The default flag-name completer has no path fallback inside =.
        let seed = ($word | %str substring ($prefix | %str length)..)
        return ($seed | %commandline complete --type path --detailed | %each {|file|
            {
                value: (leapmux-completion-text ($prefix + (leapmux-completion-word $file.value)))
                kind: $file.kind
                append_whitespace: ($file.append_whitespace? | %default false)
            }
        })
    }

    let append_whitespace = ($directive | %bits and 2) == 0
    $entries | %each {|line|
        let fields = ($line | %split row --number 2 (char tab))
        let value = if ($fields.0 | %str starts-with $prefix) { $fields.0 } else { $prefix + $fields.0 }
        {value: (leapmux-completion-text $value), description: ($fields.1? | %default ''), append_whitespace: $append_whitespace}
    }
}

@complete leapmux-complete
export extern leapmux [...args: string]

@complete leapmux-complete
export extern lmx [...args: string]
