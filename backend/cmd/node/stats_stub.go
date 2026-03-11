//go:build !linux

package main

import "github.com/ojhapranjal26/featherdeploy/backend/internal/heartbeat"

func collectStats() heartbeat.BrainStats { return heartbeat.BrainStats{} }

