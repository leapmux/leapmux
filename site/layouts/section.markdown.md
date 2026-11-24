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
{{- /* Drop Hextra {{< cards >}} navigation grids on section index pages because ## Pages / ## Sections below already emit canonical .md links with descriptions. */ -}}
{{- $body = replaceRE `(?s)\{\{[<%]\s*cards\b[^>]*[%>]\}\}.*?\{\{[<%]\s*/cards\s*[%>]\}\}` "" $body -}}
{{- /* Strip remaining Hextra layout shortcode wrappers so plain Markdown stays clean. */ -}}
{{- $body = replaceRE `\{\{[<%]\s*/?(?:cards|steps|tabs|tab|details|agent-ticker|screenshot|video-clip)[^>]*[%>]\}\}` "" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*card\s+[^>]*\btitle="([^"]+)"[^>]*\blink="([^"]+)"[^>]*[%>]\}\}` "- [$1]($2)" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*card\s+[^>]*\blink="([^"]+)"[^>]*\btitle="([^"]+)"[^>]*[%>]\}\}` "- [$2]($1)" $body -}}
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
{{ if $body }}
{{ $body }}
{{ end }}
{{- if .RegularPages }}

## Pages
{{- range .RegularPages }}
{{- $url := .Permalink }}{{- with .OutputFormats.Get "markdown" }}{{- $url = .Permalink }}{{- end }}
- [{{ .Title }}]({{ $url }}){{ with .Description }}: {{ . }}{{ end }}
{{- end }}
{{- end }}
{{- if .Sections }}

## Sections
{{- range .Sections }}
{{- $url := .Permalink }}{{- with .OutputFormats.Get "markdown" }}{{- $url = .Permalink }}{{- end }}
- [{{ .Title }}]({{ $url }}){{ with .Description }}: {{ . }}{{ end }}
{{- end }}
{{- end }}
