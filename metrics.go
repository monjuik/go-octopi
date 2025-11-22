package gooctopi

import (
	"expvar"
	"runtime"
	"runtime/metrics"
	"strconv"
)

const (
	metricWalletLRUCapacityEnv = "OCTOPI_METRICS_WALLET_LRU_CAP"

	heapObjectsMetric = "/memory/classes/heap/objects:bytes"
)

var (
	appResponseCounts      = expvar.NewMap("app_http_responses_total")
	externalResponseCounts = expvar.NewMap("external_http_responses_total")
	walletUniqueRecent     = expvar.NewInt("wallet_unique_recent")

	knownRuntimeMetrics = buildRuntimeMetricSet()
)

func init() {
	expvar.Publish("process_mem_bytes", expvar.Func(func() any { return readProcessMemory() }))
	expvar.Publish("heap_inuse_bytes", expvar.Func(func() any { return readHeapInUse() }))
	expvar.Publish("runtime_heap_objects_bytes", expvar.Func(func() any { return readRuntimeHeapObjects() }))
}

func buildRuntimeMetricSet() map[string]struct{} {
	set := make(map[string]struct{})
	for _, sample := range metrics.All() {
		set[sample.Name] = struct{}{}
	}
	return set
}

func readProcessMemory() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Sys
}

func readHeapInUse() uint64 {
	if value, ok := readRuntimeUint64("/memory/classes/heap/used:bytes"); ok {
		return value
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

func readRuntimeHeapObjects() uint64 {
	if value, ok := readRuntimeUint64(heapObjectsMetric); ok {
		return value
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func readRuntimeUint64(name string) (uint64, bool) {
	if _, ok := knownRuntimeMetrics[name]; !ok {
		return 0, false
	}

	sample := metrics.Sample{Name: name}
	metrics.Read([]metrics.Sample{sample})

	switch sample.Value.Kind() {
	case metrics.KindUint64:
		return sample.Value.Uint64(), true
	case metrics.KindFloat64:
		return uint64(sample.Value.Float64()), true
	default:
		return 0, false
	}
}

func incrementResponseCount(counter *expvar.Map, code int) {
	if counter == nil {
		return
	}
	counter.Add(strconv.Itoa(code), 1)
}
