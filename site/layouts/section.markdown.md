{{- $body := partialCached "clean-markdown-body.html" . .RelPermalink -}}
{{- $pages := where .RegularPages "Params.llms" "ne" false -}}
{{- $sections := where .Sections "Params.llms" "ne" false -}}
# {{ .Title }}
{{ with .Description }}
> {{ . }}
{{ end }}
Canonical URL: {{ .Permalink }}
{{ if $body }}
{{ $body }}
{{ end }}
{{- if $pages }}

## Pages
{{- range $pages }}
{{- $url := partial "markdown-permalink.html" . }}
- [{{ .Title }}]({{ $url }}){{ with .Description }}: {{ . }}{{ end }}
{{- end }}
{{- end }}
{{- if $sections }}

## Sections
{{- range $sections }}
{{- $url := partial "markdown-permalink.html" . }}
- [{{ .Title }}]({{ $url }}){{ with .Description }}: {{ . }}{{ end }}
{{- end }}
{{- end }}
