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

package approximateprefix

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	fwksched "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/scheduling"
)

func TestRegisterMetrics(t *testing.T) {
	resetMetrics()
	t.Cleanup(resetMetrics)

	registry := prometheus.NewRegistry()
	require.NoError(t, registerMetrics(registry))
	require.NoError(t, registerMetrics(registry))
}

func TestRecordPrefixCacheMetrics(t *testing.T) {
	resetMetrics()
	t.Cleanup(resetMetrics)

	recordPrefixCacheSize("test-plugin", "test-type", 4096)
	recordPrefixCacheMatch("test-plugin", "test-type", 10, 20)
	recordPrefixCacheMatch("test-plugin", "test-type", 0, 0)

	require.Equal(t, float64(4096), testutil.ToFloat64(llmdPrefixCacheSize.WithLabelValues("test-plugin", "test-type")))

	hitRatio, err := getHistogram(llmdPrefixCacheHitRatio, "test-plugin", "test-type")
	require.NoError(t, err)
	require.Equal(t, uint64(1), hitRatio.GetSampleCount())
	require.Equal(t, 0.5, hitRatio.GetSampleSum())

	hitLength, err := getHistogram(llmdPrefixCacheHitLength, "test-plugin", "test-type")
	require.NoError(t, err)
	require.Equal(t, uint64(2), hitLength.GetSampleCount())
	require.Equal(t, float64(10), hitLength.GetSampleSum())
}

func getHistogram(histogram *prometheus.HistogramVec, labelValues ...string) (*dto.Histogram, error) {
	metric, err := histogram.GetMetricWithLabelValues(labelValues...)
	if err != nil {
		return nil, err
	}
	dtoMetric := &dto.Metric{}
	if err := metric.(prometheus.Histogram).Write(dtoMetric); err != nil {
		return nil, err
	}
	return dtoMetric.GetHistogram(), nil
}

func resetMetrics() {
	llmdPrefixCacheSize.Reset()
	llmdPrefixCacheHitRatio.Reset()
	llmdPrefixCacheHitLength.Reset()
}

// PreRequest reports the prefix hit for the chosen endpoint in tokens, so the
// figure is comparable with the cached-token count the model server returns.
func TestPreRequestRecordsPredictedCachedTokens(t *testing.T) {
	disableMinBlockSizeClamp(t)

	const (
		name      = "approx-predicted-records"
		blockSize = 2
	)
	cfg := config{
		BlockSizeTokens:        blockSize,
		MaxPrefixBlocksToMatch: defaultMaxPrefixBlocks,
		LRUCapacityPerServer:   defaultLRUCapacityPerServer,
	}
	p, err := newDataProducer(context.Background(), name, cfg, testHandle())
	require.NoError(t, err)

	endpoint := fwksched.NewEndpoint(
		&fwkdl.EndpointMetadata{ID: k8stypes.NamespacedName{Name: "pod1", Namespace: "default"}},
		fwkdl.NewMetrics(), fwkdl.NewAttributes())
	endpoints := []fwksched.Endpoint{endpoint}
	result := &fwksched.SchedulingResult{
		PrimaryProfileName: "default",
		ProfileResults: map[string]*fwksched.ProfileRunResult{
			"default": {TargetEndpoints: endpoints},
		},
	}

	// Seed the indexer: nothing is cached yet, so the prediction is zero.
	tokens := []uint32{1, 2, 3, 4}
	seed := &fwksched.InferenceRequest{RequestID: "seed", TargetModel: "m", Body: tokenizedBody(tokens)}
	require.NoError(t, p.Produce(context.Background(), seed, endpoints))
	require.NoError(t, p.PreRequest(context.Background(), seed, result))
	p.wg.Wait()
	require.Equal(t, float64(0), predictedCachedTokensSum(t, name))

	// The same prompt now matches every block on the endpoint that was chosen.
	repeat := &fwksched.InferenceRequest{RequestID: "repeat", TargetModel: "m", Body: tokenizedBody(tokens)}
	require.NoError(t, p.Produce(context.Background(), repeat, endpoints))
	require.NoError(t, p.PreRequest(context.Background(), repeat, result))
	p.wg.Wait()

	assert.Equal(t, float64(len(tokens)), predictedCachedTokensSum(t, name))
}

// predictedCachedTokensSum reads the shared prefix metric out of the registry it
// is registered against, since the metric lives in another package.
func predictedCachedTokensSum(t *testing.T, pluginName string) float64 {
	t.Helper()
	families, err := ctrlmetrics.Registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "llm_d_epp_prefix_predicted_cached_tokens" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "plugin_name" && label.GetValue() == pluginName {
					return metric.GetHistogram().GetSampleSum()
				}
			}
		}
	}
	return 0
}
