package cycle

import (
	"github.com/olekukonko/ll"
	"reflect"
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	visitKeyPool = sync.Pool{
		New: func() interface{} {
			return &VisitKey{}
		},
	}
)

type VisitKey struct {
	ptr uintptr
	typ reflect.Type // Changed to store Type directly
}

type Detector struct {
	visited       map[VisitKey]reflect.Value
	logger        *ll.Logger
	mu            sync.RWMutex
	metrics       Metrics
	enablePool    bool
	enableMetrics bool
}

type Metrics struct {
	Hits       uint64 `json:"hits"`
	Misses     uint64 `json:"misses"`
	Tracked    uint64 `json:"tracked"`
	Skipped    uint64 `json:"skipped"`
	Resets     uint64 `json:"resets"`
	Collisions uint64 `json:"collisions"`
}

type Option func(*Detector)

func WithLogger(logger *ll.Logger) Option {
	return func(d *Detector) {
		d.logger = logger
	}
}

func WithPool(enabled bool) Option {
	return func(d *Detector) {
		d.enablePool = enabled
	}
}

func WithMetrics(enabled bool) Option {
	return func(d *Detector) {
		d.enableMetrics = enabled
	}
}

func New(opts ...Option) *Detector {
	d := &Detector{
		visited: make(map[VisitKey]reflect.Value),
		logger:  ll.New("cycle"),
	}

	for _, opt := range opts {
		opt(d)
	}
	return d
}

func (d *Detector) Check(srcPtr uintptr, srcElemType reflect.Type) (reflect.Value, bool) {
	if srcPtr == 0 {
		if d.enableMetrics {
			atomic.AddUint64(&d.metrics.Skipped, 1)
		}
		return reflect.Value{}, false
	}

	var vk *VisitKey
	if d.enablePool {
		vk = visitKeyPool.Get().(*VisitKey)
		defer visitKeyPool.Put(vk)
		*vk = VisitKey{ptr: srcPtr, typ: srcElemType} // Store type directly
	} else {
		vk = &VisitKey{ptr: srcPtr, typ: srcElemType} // Store type directly
	}

	d.mu.RLock()
	existingDest, ok := d.visited[*vk]
	d.mu.RUnlock()

	if ok {
		if d.enableMetrics {
			atomic.AddUint64(&d.metrics.Hits, 1)
		}
		var existingDestPtr uintptr
		if existingDest.IsValid() {
			switch {
			case (existingDest.Kind() == reflect.Ptr || existingDest.Kind() == reflect.UnsafePointer) && !existingDest.IsNil():
				existingDestPtr = existingDest.Pointer()
			case existingDest.CanAddr():
				existingDestPtr = existingDest.UnsafeAddr()
			}
		}
		if d.logger != nil {
			d.logger.Debugf("Check: FOUND srcPtr %p (ElemType %s) -> existingDest %s (addr/ptr: %p)",
				unsafe.Pointer(srcPtr), srcElemType.String(), existingDest.Type().String(), unsafe.Pointer(existingDestPtr))
		}
	} else if d.enableMetrics {
		atomic.AddUint64(&d.metrics.Misses, 1)
	}

	return existingDest, ok
}

func (d *Detector) Track(srcPtr uintptr, srcElemType reflect.Type, destVal reflect.Value) {
	if srcPtr == 0 {
		if d.enableMetrics {
			atomic.AddUint64(&d.metrics.Skipped, 1)
		}
		if d.logger != nil {
			d.logger.Debugf("Track: skipping nil pointer (ElemType %s)", srcElemType.String())
		}
		return
	}

	var vk *VisitKey
	if d.enablePool {
		vk = visitKeyPool.Get().(*VisitKey)
		defer visitKeyPool.Put(vk)
		*vk = VisitKey{ptr: srcPtr, typ: srcElemType} // Store type directly
	} else {
		vk = &VisitKey{ptr: srcPtr, typ: srcElemType} // Store type directly
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.visited[*vk]; !exists {
		if !destVal.IsValid() {
			if d.logger != nil {
				d.logger.Warnf("Track: Attempted to track invalid destVal for srcPtr %p (ElemType %s)",
					unsafe.Pointer(srcPtr), srcElemType.String())
			}
			return
		}

		d.visited[*vk] = destVal
		if d.enableMetrics {
			atomic.AddUint64(&d.metrics.Tracked, 1)
		}

		var destValAddr uintptr
		if (destVal.Kind() == reflect.Ptr || destVal.Kind() == reflect.UnsafePointer) && !destVal.IsNil() {
			destValAddr = destVal.Pointer()
		} else if destVal.CanAddr() {
			destValAddr = destVal.UnsafeAddr()
		}
		if d.logger != nil {
			d.logger.Debugf("Track: STORED srcPtr %p (ElemType %s) -> destVal %s (addr/ptr: %p)",
				unsafe.Pointer(srcPtr), srcElemType.String(), destVal.Type().String(), unsafe.Pointer(destValAddr))
		}
	} else {
		if d.enableMetrics {
			atomic.AddUint64(&d.metrics.Collisions, 1)
		}
		if d.logger != nil {
			d.logger.Debugf("Track: srcPtr %p (ElemType %s) already tracked",
				unsafe.Pointer(srcPtr), srcElemType.String())
		}
	}
}

func (d *Detector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.visited = make(map[VisitKey]reflect.Value)
	if d.enableMetrics {
		atomic.AddUint64(&d.metrics.Resets, 1)
	}
	d.logger.Debug("Reset: cleared all tracked pointers")
}

func (d *Detector) Count() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.visited)
}

func (d *Detector) Metrics() Metrics {
	if !d.enableMetrics {
		return Metrics{}
	}
	return Metrics{
		Hits:       atomic.LoadUint64(&d.metrics.Hits),
		Misses:     atomic.LoadUint64(&d.metrics.Misses),
		Tracked:    atomic.LoadUint64(&d.metrics.Tracked),
		Skipped:    atomic.LoadUint64(&d.metrics.Skipped),
		Resets:     atomic.LoadUint64(&d.metrics.Resets),
		Collisions: atomic.LoadUint64(&d.metrics.Collisions),
	}
}

func (d *Detector) SetLogger(logger *ll.Logger) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logger = logger
}
