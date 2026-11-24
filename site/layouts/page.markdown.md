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
{{- /* Strip Hextra layout shortcode wrappers so plain Markdown stays clean. */ -}}
{{- $body = replaceRE `\{\{<\s*/?(?:callout|cards|steps|tabs|tab|details|agent-logos|matrix)[^>]*>\}\}` "" $body -}}
{{- $body = replaceRE `\{\{<\s*card\s+[^>]*title="([^"]+)"[^>]*link="([^"]+)"[^>]*>\}\}` "- [$1]($2)" $body -}}
{{- $body = replaceRE `\{\{<\s*card\s+[^>]*link="([^"]+)"[^>]*title="([^"]+)"[^>]*>\}\}` "- [$2]($1)" $body -}}
{{- $body = replaceRE `\n{3,}` "\n\n" $body -}}
{{- $body = strings.TrimSpace $body -}}
# {{ .Title }}
{{ with .Description }}
> {{ . }}
{{ end }}
Canonical URL: {{ .Permalink }}

{{ $body }}
