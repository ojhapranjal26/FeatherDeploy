//go:build !linux

package main

import "github.com/deploy-paas/backend/internal/heartbeat"

func collectStats() heartbeat.BrainStats { return heartbeat.BrainStats{} }
