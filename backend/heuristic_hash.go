package main

import "math"

const fnv64Offset = 1469598103934665603
const fnv64Prime = 1099511628211

func resolvedHeuristicConfig(config Config) HeuristicConfig {
	defaults := DefaultConfig().Heuristics
	heuristics := config.Heuristics
	if heuristics == (HeuristicConfig{}) {
		return defaults
	}
	if heuristics.Open4 == 0 {
		heuristics.Open4 = defaults.Open4
	}
	if heuristics.Closed4 == 0 {
		heuristics.Closed4 = defaults.Closed4
	}
	if heuristics.Broken4 == 0 {
		heuristics.Broken4 = defaults.Broken4
	}
	if heuristics.Open3 == 0 {
		heuristics.Open3 = defaults.Open3
	}
	if heuristics.Broken3 == 0 {
		heuristics.Broken3 = defaults.Broken3
	}
	if heuristics.Closed3 == 0 {
		heuristics.Closed3 = defaults.Closed3
	}
	if heuristics.Open2 == 0 {
		heuristics.Open2 = defaults.Open2
	}
	if heuristics.Broken2 == 0 {
		heuristics.Broken2 = defaults.Broken2
	}
	if heuristics.ForkOpen3 == 0 {
		heuristics.ForkOpen3 = defaults.ForkOpen3
	}
	if heuristics.ForkFourPlus == 0 {
		heuristics.ForkFourPlus = defaults.ForkFourPlus
	}
	if heuristics.CaptureNow == 0 {
		heuristics.CaptureNow = defaults.CaptureNow
	}
	if heuristics.CaptureDoubleThreat == 0 {
		heuristics.CaptureDoubleThreat = defaults.CaptureDoubleThreat
	}
	if heuristics.CaptureNearWin == 0 {
		heuristics.CaptureNearWin = defaults.CaptureNearWin
	}
	if heuristics.CaptureInTwo == 0 {
		heuristics.CaptureInTwo = defaults.CaptureInTwo
	}
	if heuristics.HangingPair == 0 {
		heuristics.HangingPair = defaults.HangingPair
	}
	if heuristics.CaptureWinSoonScale == 0 {
		heuristics.CaptureWinSoonScale = defaults.CaptureWinSoonScale
	}
	if heuristics.CaptureInTwoLimit <= 0 {
		heuristics.CaptureInTwoLimit = defaults.CaptureInTwoLimit
	}
	return heuristics
}

func heuristicHash(config HeuristicConfig) uint64 {
	hash := uint64(fnv64Offset)
	mix := func(value float64) {
		if value == 0 {
			value = 0
		}
		bits := math.Float64bits(value)
		for i := 0; i < 8; i++ {
			hash ^= uint64(byte(bits >> (8 * i)))
			hash *= fnv64Prime
		}
	}
	mix(config.Open4)
	mix(config.Closed4)
	mix(config.Broken4)
	mix(config.Open3)
	mix(config.Broken3)
	mix(config.Closed3)
	mix(config.Open2)
	mix(config.Broken2)
	mix(config.ForkOpen3)
	mix(config.ForkFourPlus)
	mix(config.CaptureNow)
	mix(config.CaptureDoubleThreat)
	mix(config.CaptureNearWin)
	mix(config.CaptureInTwo)
	mix(config.HangingPair)
	mix(config.CaptureWinSoonScale)
	mix(float64(config.CaptureInTwoLimit))
	return hash
}

func heuristicHashFromConfig(config Config) uint64 {
	return heuristicHash(resolvedHeuristicConfig(config))
}
