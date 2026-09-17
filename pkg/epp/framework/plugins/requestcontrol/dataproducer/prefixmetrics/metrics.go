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

// Package prefixmetrics holds the metrics every prefix-aware data producer
// emits, so approximate and precise deployments report prefix-cache prediction
// under one metric name.
package prefixmetrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	compbasemetrics "k8s.io/component-base/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	metricsutil "github.com/llm-d/llm-d-router/pkg/common/observability/metrics"
	eppmetrics "github.com/llm-d/llm-d-router/pkg/epp/metrics"
)

// predictedCachedTokens pairs with llm_d_epp_request_cached_tokens: each _sum
// over llm_d_epp_request_input_tokens_sum gives the prefix hit rate the router
// predicted and the rate the model server delivered.
var predictedCachedTokens = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Subsystem: eppmetrics.LLMDRouterEndpointPickerSubsystem,
		Name:      "prefix_predicted_cached_tokens",
		Help: metricsutil.HelpMsgWithStability(
			"Prompt tokens the producer predicted the scheduler's chosen endpoint holds in its prefix cache, per request.",
			compbasemetrics.ALPHA),
		Buckets: metricsutil.TokenCountBuckets,
	},
	[]string{"plugin_name", "plugin_type"},
)

var registerOnce sync.Once

// Register makes the shared prefix metrics collectable. Every prefix producer
// instance calls it; the first call registers.
func Register() {
	registerOnce.Do(func() {
		metrics.Registry.MustRegister(predictedCachedTokens)
	})
}

// RecordPredictedCachedTokens records the prompt tokens the producer expects
// the scheduler's chosen endpoint to serve from its prefix cache.
func RecordPredictedCachedTokens(pluginName, pluginType string, tokens int) {
	predictedCachedTokens.WithLabelValues(pluginName, pluginType).Observe(float64(tokens))
}
