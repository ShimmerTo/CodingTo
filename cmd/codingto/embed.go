package main

import "embed"

// Assets contains the built frontend (./frontend/dist relative to this file)
// served by the Wails app. It lives in package main so that Go's //go:embed
// can resolve the frontend build output, which vite writes into
// cmd/codingto/frontend/dist (see frontend/vite.config.js outDir).
//
//go:embed all:frontend/dist
var Assets embed.FS
