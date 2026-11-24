{{- $body := .RawContent -}}
{{- /* Resolve duration shortcodes from site.Params.durations. */ -}}
{{- range $k, $v := site.Params.durations -}}
  {{- $body = replace $body (printf "{{< duration %s digits >}}" $k) $v.digits -}}
  {{- $body = replace $body (printf "{{< duration %s words >}}" $k) $v.words -}}
  {{- $body = replace $body (printf "{{< duration %s >}}" $k) $v.words -}}
{{- end -}}
{{- /* Resolve agent-provider-count shortcode from feature_matrix checklist. */ -}}
{{- $checklist := index hugo.Data "feature_matrix" "checklist" -}}
{{- $count := 0 -}}
{{- range $group := $checklist.providerGroups -}}
  {{- $count = add $count (len $group) -}}
{{- end -}}
{{- $body = replace $body "{{< agent-provider-count >}}" (printf "%d" $count) -}}
{{- /* Preserve useful structure when converting interactive shortcodes to Markdown. */ -}}
{{- $body = replaceRE `\{\{[<%]\s*tab\b[^>]*\bname="([^"]+)"[^>]*[%>]\}\}` "\n\n#### $1\n\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*details\b[^>]*\btitle="([^"]+)"[^>]*[%>]\}\}` "\n\n**$1**\n\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*screenshot\b[^>]*\bcaption="([^"]+)"[^>]*[%>]\}\}` "\n\n*[Screenshot: $1]*\n\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*screenshot\b[^>]*\balt="([^"]+)"[^>]*[%>]\}\}` "\n\n*[Screenshot: $1]*\n\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*copy-prompt\b[^>]*\blabel="([^"]+)"[^>]*[%>]\}\}\s*` "\n\n**$1**\n\n```text\n" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*copy-prompt\b[^>]*[%>]\}\}\s*` "\n\n**Prompt for your coding agent**\n\n```text\n" $body -}}
{{- $body = replaceRE `\s*\{\{[<%]\s*/copy-prompt\s*[%>]\}\}` "\n```\n\n" $body -}}
{{- /* Strip remaining Hextra layout shortcode wrappers so plain Markdown stays clean. */ -}}
{{- $body = replaceRE `\{\{[<%]\s*/?(?:callout|cards|steps|tabs|tab|details|agent-logos|screenshot)[^>]*[%>]\}\}` "" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*card\s+[^>]*link="([^"]+)"[^>]*title="([^"]+)"[^>]*[%>]\}\}` "- [$2]($1)" $body -}}
{{- $body = replaceRE `\{\{[<%]\s*card\s+[^>]*title="([^"]+)"[^>]*link="([^"]+)"[^>]*[%>]\}\}` "- [$1]($2)" $body -}}
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
