/*
Copyright 2026 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package prefixmetrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every producer instance calls Register, so repeated calls must not panic.
func TestRegisterIsIdempotent(t *testing.T) {
	assert.NotPanics(t, func() {
		Register()
		Register()
	})
}

// A zero prediction is a real observation: the router expected no cache hit.
func TestRecordPredictedCachedTokens(t *testing.T) {
	predictedCachedTokens.Reset()
	t.Cleanup(predictedCachedTokens.Reset)

	RecordPredictedCachedTokens("test-plugin", "test-type", 512)
	RecordPredictedCachedTokens("test-plugin", "test-type", 0)

	got, err := predictedHistogram("test-plugin", "test-type")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), got.GetSampleCount())
	assert.Equal(t, float64(512), got.GetSampleSum())
}

func predictedHistogram(labelValues ...string) (*dto.Histogram, error) {
	observer, err := predictedCachedTokens.GetMetricWithLabelValues(labelValues...)
	if err != nil {
		return nil, err
	}
	metric := &dto.Metric{}
	if err := observer.(prometheus.Histogram).Write(metric); err != nil {
		return nil, err
	}
	return metric.GetHistogram(), nil
}
