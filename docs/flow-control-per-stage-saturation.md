# Per-Stage Saturation Detectors in Flow Control

## Summary

Tracked in
[#2585](https://github.com/llm-d/llm-d-router/issues/2585). Flow control
saturation gating is stage-aware since
[#2221](https://github.com/llm-d/llm-d-router/pull/2221), but it still uses a
single detector instance for every stage. This document designs an extension
that lets each pipeline stage use its own detector instance, keeps the
existing single-detector configuration working unchanged, and leaves room for
future stages and combination modes.

## Background and problem

In a prefill/decode deployment the stages have different resource profiles:
prefill is token-bound and decode is often request-bound. The `concurrency-detector`
supports `hybrid` mode to cover both dimensions with one instance, but one
instance has one set of limits (`maxConcurrency`, `maxTokenConcurrency`). A
deployment cannot today say "prefill gates on tokens with limit X, decode
gates on requests with limit Y" because `flowControl.saturationDetector` takes
exactly one `pluginRef`.

The flow control processor already partitions endpoints per stage and calls
the detector once per stage (`pkg/epp/flowcontrol/controller/internal/processor.go`).
What is missing is the ability to select a different detector for each call.

## Goals

- Allow `flowControl.saturationDetector` to map a stage to a detector instance.
- Allow gating on a single stage (for example prefill only) instead of always
  `max(prefill, decode)`.
- Preserve the behavior of every existing configuration byte-for-byte.
- Keep the `SaturationDetector` interface unchanged so existing detectors
  (concurrency, utilization) need no code change.
- Support future stages (for example encode) and future per-stage parameters
  without a config schema rewrite.

## Non-goals

- Changing the per-stage partition scheme (`partitionEndpoints`).
- Combining per-stage values other than `max` or a single selected stage
  (no weighted sums, no min).
- Per-stage detectors in the legacy admission controller (flow control gate
  disabled). The legacy path keeps the default detector.
- Per-stage stale-metrics accounting; that is tracked separately in
  [#2475](https://github.com/llm-d/llm-d-router/issues/2475).

## Design

### Configuration

Extend `SaturationDetectorConfig` in
`apix/config/v1alpha1/endpointpickerconfig_types.go`:

```go
// SaturationDetectorConfig contains the configuration for a saturation detector.
type SaturationDetectorConfig struct {
	// PluginRef specifies the default saturation detector instance. It is
	// used for every stage that has no specific override and for the legacy
	// admission path. If unspecified, "utilization-detector" is used.
	PluginRef string `json:"pluginRef,omitempty"`

	// Stages overrides the detector per pipeline stage. Keys are stage names
	// ("prefill", "decode"); values reference plugin instances declared in
	// the top-level Plugins section. A stage without an override uses
	// PluginRef.
	Stages map[string]string `json:"stages,omitempty"`

	// GateOn selects which stage's signal gates dispatch. "max" (default)
	// gates on the highest stage signal. Any other value must be a stage
	// name; dispatch then gates on that stage alone.
	GateOn string `json:"gateOn,omitempty"`
}
```

Example:

```yaml
plugins:
  - type: concurrency-detector
    name: prefill-concurrency
    parameters:
      concurrencyMode: "tokens"
      maxTokenConcurrency: 300000
  - type: concurrency-detector
    name: decode-concurrency
    parameters:
      concurrencyMode: "requests"
      maxConcurrency: 64
  - type: utilization-detector

flowControl:
  saturationDetector:
    pluginRef: utilization-detector
    stages:
      prefill: prefill-concurrency
      decode: decode-concurrency
    gateOn: max
```

`gateOn: prefill` is how a deployment restricts gating to the prefill stage.

### Resolution

The config loader (`pkg/epp/config/loader/configloader.go`) resolves each
stage reference to a `fwkfc.SaturationDetector` at load time, type-asserting
every referenced plugin, and stores the map on `flowcontrol.Config`. The
existing single-detector field stays and remains the default fallback.

Validations:

- `stages` keys must belong to the known stage set. Today the set is
  `{"prefill", "decode"}`; the set lives in one place so a future stage is
  added by editing that set plus `partitionEndpoints`.
- Every stage reference must name an instantiated plugin that implements
  `fwkfc.SaturationDetector`.
- `gateOn`, when not empty, must be `"max"` or a known stage name.
- A stage key with an empty string value is a validation error, not a
  silent fallback.

### Evaluation semantics

The processor keeps partitioning endpoints per stage. For each non-empty stage
partition it selects `stageDetectors[stage]` when configured, otherwise the
default detector, and records the stage metric as today. The effective
saturation is:

- `max(prefill, decode)` when `gateOn` is `max` (the default);
- the selected stage's saturation when `gateOn` names a stage.

Empty partitions keep their current behavior: the stage metric series is
deleted and the stage is skipped. If `gateOn` names a stage and that stage
partition is empty, the effective saturation is 1.0 (fail closed), because
gating on a stage that has no capacity must not open the gate. If both
partitions are empty, the effective saturation falls back to the default
detector over the whole pool, as today.

### Where this does not apply

- **Legacy admission controller** (`pkg/epp/requestcontrol/admission.go`):
  uses the default detector over all candidates, unchanged. When the flow
  control gate is disabled and `stages` is set, the loader warns that the
  per-stage settings are ignored (mirrors the existing warning for other
  flowControl settings).
- **Scheduling filters**: the `concurrency-detector`/`utilization-detector`
  filter path inside profiles is per-endpoint and stage-agnostic; it is not
  changed.

### Metrics

No new metrics. `flow_control_pool_saturation{stage="prefill"}` and
`{stage="decode"}` keep their series, each now sourced from its stage
detector. `flow_control_stale_endpoints` continues to be written by whichever
utilization detector is invoked last; per-stage stale accounting remains
tracked in [#2475](https://github.com/llm-d/llm-d-router/issues/2475).

## Alternatives considered

### A. Configuration extension plus processor resolution (chosen)

The stages already exist in the processor; the only missing piece is the
detector selection. Extending `SaturationDetectorConfig` and resolving refs at
load time keeps the change local to config loading and the processor's
stage loop. Detectors stay stage-agnostic: they receive the endpoint subset
and return one number.

### B. New wrapper plugin, for example `stage-aware-saturation-detector`

A plugin instance would need to receive the per-stage endpoint subsets to be
useful, but the processor owns the partition and calls `Saturation` per stage.
A wrapper would either duplicate the partition logic or require the processor
to defer to it, replacing the processor's role with a second implementation.
It also cannot override the processor's per-stage metric recording, so it
would need to replicate that too. Rejected as a duplicate control point.

### C. Per-stage detector instances declared as plugins with their own
   config, resolved by name (part of A)

This is what the references compose with: detectors remain normal plugins
declared in the top-level `plugins` list, so future per-stage parameters are
just more named instances. No new plugin type is required.

## Interplay with conditional PDD

A deployment can run P/D disaggregation conditionally: the
`prefix-based-pd-decider` routes only requests that need remote prefill to the
prefill stage, and serves the rest monolithically on decode-capable pods
(which may be labeled `prefill-decode`). Such a fleet wants the following
behavior: when the prefill pool is saturated, requests that do not need remote
prefill are still dispatched to an unsaturated decode pod, while requests that
do need prefill stay gated.

The per-stage detector design above does not provide that behavior. It gates
at the pool level and is request-agnostic:

- `gateOn: max` (default) or `gateOn: prefill`: prefill saturation >= 1.0
  blocks every request in the band, including monolithic ones.
- `gateOn: decode`: monolithic requests pass, but disaggregated requests also
  pass, so the prefill pool is no longer protected at all.

The cause is ordering: the gate runs in the dispatch cycle before the request
is scheduled, but the "needs remote prefill" decision is produced inside the
scheduler by the disagg profile handler, after dispatch. The flow control
queues therefore cannot know an item's stage requirement at gate time.

Supporting conditional PDD requires extending the design with per-item stage
requirement gating:

1. Compute the requirement before the gate. Run the PD decider's prefix-state
   read at enqueue time and memoize the outcome on the flow item. Prefix
   state can change while the item waits, so the decision may be stale; the
   conditional-decode gate (HTTP 412) remains the runtime backstop.
2. Or evaluate after selection. Pick an item via fairness/ordering first,
   then gate on that item: needs prefill and prefill saturated -> keep queued
   (with defined HoL semantics for the band); decode-only and decode
   unsaturated -> dispatch even when prefill is saturated.

This is a follow-up feature; the per-stage detector design is a prerequisite
for it. Tracking it separately avoids coupling request-requirement analysis
to detector selection.

## Backward compatibility

| Configuration | Behavior |
|---|---|
| `saturationDetector.pluginRef` only | Unchanged: one detector, all stages. |
| `saturationDetector` unset | Unchanged: `utilization-detector` default. |
| Deprecated top-level `saturationDetector` | Unchanged: feeds `pluginRef` as today. |
| `stages` set, gate off (no `flowControl` gate) | Warning; `pluginRef` used; `stages`/`gateOn` ignored. |

The new fields are optional and additive; no existing config changes meaning.

## Extensibility

- **New stage** (for example encode): add the key to the known-stage set and
  to `partitionEndpoints`; `stages.enc*` refs then work without a schema
  change because the map is keyed by string.
- **Per-stage parameters**: declare a named detector instance with its own
  parameters (already possible) and reference it.
- **New combined gating rule**: `gateOn` is a string; adding a value is a
  validation-set change plus a selection branch.
- **Per-stage detector behavior changes**: implementations of
  `SaturationDetector` are unchanged; a detector that wants stage-aware
  behavior can read the endpoint subset it is handed.
