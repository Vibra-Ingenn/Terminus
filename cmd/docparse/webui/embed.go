package webui

import "embed"

// Static assets for the local Terminus screen (no network downloads).
//
//go:embed static/*
var Static embed.FS
