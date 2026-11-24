{{- $body := strings.TrimSpace .RawContent -}}
{{- $body = replaceRE `\{\{<\s*/?(?:cards|callout)[^>]*>\}\}` "" $body -}}
{{- $body = replaceRE `\{\{<\s*card\s+[^>]*link="([^"]+)"[^>]*title="([^"]+)"[^>]*>\}\}` "- [$2]($1)" $body -}}
{{- $body = replaceRE `\{\{<\s*card\s+[^>]*title="([^"]+)"[^>]*link="([^"]+)"[^>]*>\}\}` "- [$1]($2)" $body -}}
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
