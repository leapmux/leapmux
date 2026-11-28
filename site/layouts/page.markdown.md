{{- $body := partial "clean-markdown-body.html" . -}}
# {{ .Title }}
{{ with .Description }}
> {{ . }}
{{ end }}
Canonical URL: {{ .Permalink }}

{{ $body }}
