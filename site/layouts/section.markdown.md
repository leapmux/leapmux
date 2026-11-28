{{- $body := partial "clean-markdown-body.html" . -}}
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
