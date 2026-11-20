# Load with eval (leapmux completion elvish | slurp).
use os
use re
use str

# Cobra's final :N field uses bits 1 for an error, 2 to omit a space,
# and 4 to suppress filename completion.
fn leapmux-completion-flag {|directive flag|
    >= (% $directive (* $flag 2)) $flag
}

fn leapmux-complete {|command @args|
    if (== (count $args) 0) { return }
    var lines = []
    try {
        set lines = [((external $command) __complete $@args 2>$os:dev-null)]
    } catch {
        return
    }
    if (== (count $lines) 0) { return }
    var status = $lines[-1]
    if (not (re:match '^:[0-9]+$' $status)) { return }
    var directive = (num $status[1..])
    if (leapmux-completion-flag $directive 1) { return }

    var seed = $args[-1]
    var flag-prefix = ''
    if (str:has-prefix $seed '--') {
        var pair = [(str:split '=' $seed &max=2)]
        if (== (count $pair) 2) { set flag-prefix = $pair[0]'=' }
    }
    if (== (count $lines) 1) {
        if (leapmux-completion-flag $directive 4) { return }
        if (eq $flag-prefix '') {
            edit:complete-filename $seed
        } else {
            var path = (str:trim-prefix $seed $flag-prefix)
            for candidate [(edit:complete-filename $path)] {
                edit:complex-candidate $flag-prefix$candidate[stem] &display=$candidate[display] &code-suffix=$candidate[code-suffix]
            }
        }
        return
    }

    var suffix = ' '
    if (leapmux-completion-flag $directive 2) { set suffix = '' }
    for line $lines[..-1] {
        var fields = [(str:split "\t" $line &max=2)]
        var stem = $fields[0]
        if (not (str:has-prefix $stem $flag-prefix)) { set stem = $flag-prefix$stem }
        var display = $stem
        if (== (count $fields) 2) { set display = $stem'  '$fields[1] }
        edit:complex-candidate $stem &display=$display &code-suffix=$suffix
    }
}

set edit:completion:arg-completer[leapmux] = $leapmux-complete~
