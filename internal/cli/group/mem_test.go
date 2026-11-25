package group

import "runtime"

// runtimeGC settles the heap so a measurement is not polluted by garbage.
func runtimeGC() {
	runtime.GC()
	runtime.GC()
}

func heapAlloc() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return m.HeapAlloc
}

// keepAlive stops the compiler from treating values as dead before the final
// measurement.
func keepAlive(values ...any) {
	for _, v := range values {
		runtime.KeepAlive(v)
	}
}
