{{- $body := .RawContent -}}
{{- /* Resolve duration shortcodes from site.Params.durations. */ -}}
{{- range $k, $v := site.Params.durations -}}
  {{- $body = replace $body (printf "{{< duration %s digits >}}" $k) $v.digits -}}
  {{- $body = replace $body (printf "{{< duration %s words >}}" $k) $v.words -}}
  {{- $body = replace $body (printf "{{< duration %s >}}" $k) $v.words -}}
{{- end -}}
{{- /* Resolve agent-provider-count and agent-logos shortcodes from feature_matrix checklist. */ -}}
{{- $checklist := index hugo.Data "feature_matrix" "checklist" -}}
{{- $count := 0 -}}
{{- $providerLabels := slice -}}
{{- range $group := $checklist.providerGroups -}}
  {{- $count = add $count (len $group) -}}
  {{- range $p := $group -}}
    {{- $providerLabels = $providerLabels | append $p.label -}}
  {{- end -}}
{{- end -}}
{{- $body = replace $body "{{< agent-provider-count >}}" (printf "%d" $count) -}}
{{- $body = replaceRE `\{\{[<%]\s*agent-logos\s*[%>]\}\}` (delimit $providerLabels ", ") $body -}}
{{- /* Resolve config-file shortcodes from page bundle resources. */ -}}
{{- range .Resources -}}
  {{- $res := . -}}
  {{- $lines := split (strings.TrimRight "\n" $res.Content) "\n" -}}
  {{- $first := index $lines 0 -}}
  {{- if hasPrefix $first "# /" -}}
    {{- $path := strings.TrimPrefix "# " $first -}}
    {{- $cfgBody := strings.TrimLeft "\n" (delimit (after 1 $lines) "\n") -}}
    {{- $pat := printf `\{\{<\s*config-file\s+file="%s"\s+lang="([^"]+)"\s*>\}\}` $res.Name -}}
    {{- $rep := printf "**%s**\n\n```$1\n%s\n```" $path $cfgBody -}}
    {{- $body = replaceRE $pat $rep $body -}}
  {{- end -}}
{{- end -}}
{{- /* Render the coding-agent feature matrix as GitHub Flavored Markdown. */ -}}
{{- $body = replaceRE `\{\{[<%]\s*matrix\s*[%>]\}\}` "{{< matrix >}}" $body -}}
{{- if strings.Contains $body "{{< matrix >}}" -}}
  {{- $data := index hugo.Data "feature_matrix" -}}
  {{- $featureGroups := (index $data "features").groups -}}
  {{- $features := where (index $data "features").features "showInMatrix" true -}}
  {{- $states := $checklist.supportStates -}}
  {{- $lines := slice "Legend:" "" -}}
  {{- range $state := $states -}}
    {{- $lines = $lines | append (printf "- %s %s" $state.symbol $state.label) -}}
  {{- end -}}
  {{- $groupCount := len $checklist.providerGroups -}}
  {{- range $groupIndex, $group := $checklist.providerGroups -}}
    {{- $lines = $lines | append "" (printf "### Coding agent feature matrix (part %d of %d)" (add $groupIndex 1) $groupCount) "" -}}
    {{- $headerCells := slice "Feature" -}}
    {{- $sepCells := slice "---" -}}
    {{- range $provider := $group -}}
      {{- $label := $provider.label -}}
      {{- if $provider.userNote -}}
        {{- $label = printf "%s†" $label -}}
      {{- end -}}
      {{- $headerCells = $headerCells | append $label -}}
      {{- $sepCells = $sepCells | append "---" -}}
    {{- end -}}
    {{- $lines = $lines | append (printf "| %s |" (delimit $headerCells " | ")) -}}
    {{- $lines = $lines | append (printf "| %s |" (delimit $sepCells " | ")) -}}
    {{- range $featureGroup := $featureGroups -}}
      {{- $groupFeatures := where $features "group" $featureGroup.id -}}
      {{- if $groupFeatures -}}
        {{- $groupRow := slice (printf "**%s**" $featureGroup.label) -}}
        {{- range $group -}}
          {{- $groupRow = $groupRow | append "" -}}
        {{- end -}}
        {{- $lines = $lines | append (printf "| %s |" (delimit $groupRow " | ")) -}}
        {{- range $feature := $groupFeatures -}}
          {{- $row := index $checklist.cells $feature.id -}}
          {{- $rowCells := slice $feature.label -}}
          {{- range $provider := $group -}}
            {{- $cell := index $row $provider.id -}}
            {{- $state := index (where $states "id" $cell.support) 0 -}}
            {{- $sym := $state.symbol -}}
            {{- if $cell.userNote -}}
              {{- $sym = printf "%s†" $sym -}}
            {{- end -}}
            {{- $rowCells = $rowCells | append $sym -}}
          {{- end -}}
          {{- $lines = $lines | append (printf "| %s |" (delimit $rowCells " | ")) -}}
        {{- end -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
  {{- $lines = $lines | append "" "### Feature definitions" "" -}}
  {{- range $feature := $features -}}
    {{- $lines = $lines | append (printf "- **%s**: %s" $feature.label $feature.description) -}}
  {{- end -}}
  {{- /* Emit userNote only; never emit detailNote (maintainer-only evidence). */ -}}
  {{- $noteLines := slice -}}
  {{- range $group := $checklist.providerGroups -}}
    {{- range $provider := $group -}}
      {{- with $provider.userNote -}}
        {{- $note := replaceRE `\r?\n+` " " (strings.TrimSpace .) -}}
        {{- $noteLines = $noteLines | append (printf "- **%s**: %s" $provider.label $note) -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
  {{- range $feature := $features -}}
    {{- $row := index $checklist.cells $feature.id -}}
    {{- range $group := $checklist.providerGroups -}}
      {{- range $provider := $group -}}
        {{- $cell := index $row $provider.id -}}
        {{- with $cell.userNote -}}
          {{- $state := index (where $states "id" $cell.support) 0 -}}
          {{- $note := replaceRE `\r?\n+` " " (strings.TrimSpace .) -}}
          {{- $noteLines = $noteLines | append (printf "- **%s %s — %s**: %s" $state.symbol $provider.label $feature.label $note) -}}
        {{- end -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
  {{- if $noteLines -}}
    {{- $lines = $lines | append "" "### Notes" "" -}}
    {{- range $noteLines -}}
      {{- $lines = $lines | append . -}}
    {{- end -}}
  {{- end -}}
  {{- $matrixMd := delimit $lines "\n" -}}
  {{- $body = replace $body "{{< matrix >}}" $matrixMd -}}
{{- end -}}
{{- /* Preserve useful structure when converting interactive shortcodes to Markdown. */ -}}
{{- $body = replaceRE `\{\{[<%]\s*tab\b[^>]*\bname="([^"]+)"[^>]*[%>]\}\}` "\n\n#### $1\n\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*details\b[^>]*\btitle="([^"]+)"[^>]*[%>]\}\}` "\n\n**$1**\n\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*screenshot\b[^>]*\bcaption="([^"]+)"[^>]*[%>]\}\}` "\n\n*[Screenshot: $1]*\n\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*screenshot\b[^>]*\balt="([^"]+)"[^>]*[%>]\}\}` "\n\n*[Screenshot: $1]*\n\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*video-clip\b[^>]*\bcaption="([^"]+)"[^>]*[%>]\}\}` "\n\n*[Demo clip: $1]*\n\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*video-clip\b[^>]*\balt="([^"]+)"[^>]*[%>]\}\}` "\n\n*[Demo clip: $1]*\n\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*copy-prompt\b[^>]*\blabel="([^"]+)"[^>]*[%>]\}\}\s*` "\n\n**$1**\n\n```text\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*copy-prompt\b[^>]*[%>]\}\}\s*` "\n\n**Prompt for your coding agent**\n\n```text\n" $body -}}
{{- $body = replaceRE `\s*\{\{[<%]\s*/copy-prompt\s*[%>]\}\}` "\n```\n\n" $body -}}
{{- /* Preserve callout severity labels as Markdown blockquotes instead of silently stripping them. */ -}}
{{- $body = replaceRE `\{\{[<%]\s*callout\b[^>]*\btype="warning"[^>]*[%>]\}\}\s*` "\n\n> **Warning:** " $body -}}
{{- $body = replaceRE `\{\{[<%]\s*callout\b[^>]*\btype="error"[^>]*[%>]\}\}\s*` "\n\n> **Danger:** " $body -}}
{{- $body = replaceRE `\{\{[<%]\s*callout\b[^>]*\btype="important"[^>]*[%>]\}\}\s*` "\n\n> **Important:** " $body -}}
{{- $body = replaceRE `\{\{[<%]\s*callout\b[^>]*\btype="info"[^>]*[%>]\}\}\s*` "\n\n> **Info:** " $body -}}
{{- $body = replaceRE `\{\{[<%]\s*callout\b[^>]*[%>]\}\}\s*` "\n\n> **Note:** " $body -}}
{{- $body = replaceRE `\s*\{\{[<%]\s*/callout\s*[%>]\}\}` "\n\n" $body -}}
{{- /* Strip remaining Hextra layout shortcode wrappers so plain Markdown stays clean. */ -}}
{{- $body = replaceRE `\{\{[<%]\s*/?(?:cards|steps|tabs|tab|details|agent-ticker|screenshot|video-clip)[^>]*[%>]\}\}` "" $body -}}
{{- /* Use \b word boundaries so subtitle="..." is never misread as title="...". */ -}}
{{- $body = replaceRE `\{\{[<%]\s*card\s+[^>]*\btitle="([^"]+)"[^>]*\blink="([^"]+)"[^>]*[%>]\}\}` "- [$1]($2)" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*card\s+[^>]*\blink="([^"]+)"[^>]*\btitle="([^"]+)"[^>]*[%>]\}\}` "- [$2]($1)" $body -}}
{{- /* Strip internal HTML comments (e.g. generated-file headers) and unescape Goldmark angle-bracket entities. */ -}}
{{- $body = replaceRE `(?s)<!--.*?-->` "" $body -}}
{{- $body = replace $body "&lt;" "<" -}}
{{- $body = replace $body "&gt;" ">" -}}
{{- $body = replaceRE `\n{3,}` "\n\n" $body -}}
{{- $body = strings.TrimSpace $body -}}
# {{ .Title }}
{{ with .Description }}
> {{ . }}
{{ end }}
Canonical URL: {{ .Permalink }}

{{ $body }}
