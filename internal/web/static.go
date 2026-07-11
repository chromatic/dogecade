package web

import "embed"

//go:embed static/style.css static/buy.js static/koinu.js
var staticFS embed.FS
