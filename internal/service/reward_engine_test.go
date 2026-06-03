package service

import (
	"math"
	"testing"
)

func TestCalculateReward(t *testing.T) {
	engine := NewRewardEngine(nil, nil, nil, nil) // Dependencies not needed for this calculation

	tests := []struct {
		name              string
		computeTimeMs     int64
		gpuModel          string
		maxExhaustiveness int32
		expectedGross     float64
		expectedPlatform  float64
		expectedNet       float64
	}{
		{
			name:              "RTX 4090 standard exhaustiveness",
			computeTimeMs:     10000, // 10 seconds
			gpuModel:          "RTX 4090",
			maxExhaustiveness: 8,
			// Base: 0.0005. Sec: 10. Multiplier: 1.0. Diff: 1.0 (8/8)
			// gross = 0.0005 * 10 * 1.0 * 1.0 = 0.005
			expectedGross:    0.005,
			expectedPlatform: 0.003, // 60%
			expectedNet:      0.002, // 40%
		},
		{
			name:              "RTX 3060 high exhaustiveness",
			computeTimeMs:     20000, // 20 seconds
			gpuModel:          "RTX 3060",
			maxExhaustiveness: 16,
			// Base: 0.0005. Sec: 20. Multiplier: 0.6. Diff: 2.0 (16/8)
			// gross = 0.0005 * 20 * 0.6 * 2.0 = 0.012
			expectedGross:    0.012,
			expectedPlatform: 0.0072, // 60%
			expectedNet:      0.0048, // 40%
		},
		{
			name:              "Unknown GPU default multiplier",
			computeTimeMs:     5000, // 5 seconds
			gpuModel:          "GTX 1060",
			maxExhaustiveness: 4,
			// Base: 0.0005. Sec: 5. Multiplier: 0.5 (default). Diff: 0.5 (4/8)
			// gross = 0.0005 * 5 * 0.5 * 0.5 = 0.000625
			expectedGross:    0.000625,
			expectedPlatform: 0.000375, // 60%
			expectedNet:      0.000250, // 40%
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gross, platform, net := engine.CalculateReward(tt.computeTimeMs, tt.gpuModel, tt.maxExhaustiveness)

			if math.Abs(gross-tt.expectedGross) > 1e-6 {
				t.Errorf("expected gross %v, got %v", tt.expectedGross, gross)
			}
			if math.Abs(platform-tt.expectedPlatform) > 1e-6 {
				t.Errorf("expected platform %v, got %v", tt.expectedPlatform, platform)
			}
			if math.Abs(net-tt.expectedNet) > 1e-6 {
				t.Errorf("expected net %v, got %v", tt.expectedNet, net)
			}
		})
	}
}
