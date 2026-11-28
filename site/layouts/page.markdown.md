{{- $body := partialCached "clean-markdown-body.html" . .RelPermalink -}}
# {{ .Title }}
{{ with .Description }}
> {{ . }}
{{ end }}
Canonical URL: {{ .Permalink }}

{{ $body }}
