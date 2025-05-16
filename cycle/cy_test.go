package cycle

import (
	"reflect"
	"testing"
	"unsafe"
)

func TestCycleDetector(t *testing.T) {
	t.Run("BasicTracking", func(t *testing.T) {
		cd := New()
		var x int = 42
		ptr := unsafe.Pointer(&x)
		val := reflect.ValueOf(&x)

		// Track should store the pointer
		cd.Track(uintptr(ptr), reflect.TypeOf(x), val)

		// Check should find it
		foundVal, exists := cd.Check(uintptr(ptr), reflect.TypeOf(x))
		if !exists {
			t.Error("Expected to find tracked pointer")
		}
		if exists && foundVal.Pointer() != val.Pointer() {
			t.Errorf("Found value doesn't match tracked value: got %v, want %v",
				foundVal.Pointer(), val.Pointer())
		}
	})

	t.Run("InvalidValue", func(t *testing.T) {
		cd := New()
		var x int = 42
		ptr := unsafe.Pointer(&x)

		// Track with invalid value
		cd.Track(uintptr(ptr), reflect.TypeOf(x), reflect.Value{})

		// Shouldn't be stored
		if _, exists := cd.Check(uintptr(ptr), reflect.TypeOf(x)); exists {
			t.Error("Invalid value should not be stored")
		}
	})

	t.Run("TypeMismatch", func(t *testing.T) {
		cd := New()
		var x int = 42
		var y float64 = 3.14
		ptrX := unsafe.Pointer(&x)
		ptrY := unsafe.Pointer(&y)

		cd.Track(uintptr(ptrX), reflect.TypeOf(x), reflect.ValueOf(&x))

		// Same pointer but different type
		if _, exists := cd.Check(uintptr(ptrX), reflect.TypeOf(y)); exists {
			t.Error("Should not match with different type")
		}

		// Different pointer same type
		if _, exists := cd.Check(uintptr(ptrY), reflect.TypeOf(x)); exists {
			t.Error("Should not match with different pointer")
		}
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		cd := New()
		var x int = 42
		ptr := unsafe.Pointer(&x)
		val := reflect.ValueOf(&x)

		done := make(chan bool)
		go func() {
			cd.Track(uintptr(ptr), reflect.TypeOf(x), val)
			done <- true
		}()

		go func() {
			cd.Check(uintptr(ptr), reflect.TypeOf(x))
			done <- true
		}()

		<-done
		<-done
	})

	t.Run("Reset", func(t *testing.T) {
		cd := New()
		var x int = 42
		ptr := unsafe.Pointer(&x)

		cd.Track(uintptr(ptr), reflect.TypeOf(x), reflect.ValueOf(&x))
		cd.Reset()

		if _, exists := cd.Check(uintptr(ptr), reflect.TypeOf(x)); exists {
			t.Error("Reset should clear all tracked pointers")
		}
	})
}

func TestNilPointer(t *testing.T) {
	cd := New()
	var nilPtr *int
	val := reflect.ValueOf(nilPtr)

	// Should not track nil pointer
	cd.Track(uintptr(unsafe.Pointer(nilPtr)), reflect.TypeOf(nilPtr), val)

	if cd.Count() != 0 {
		t.Error("Nil pointer should not be tracked")
	}

	// Check should return false for nil pointer
	if _, exists := cd.Check(uintptr(unsafe.Pointer(nilPtr)), reflect.TypeOf(nilPtr)); exists {
		t.Error("Check should return false for nil pointer")
	}
}

func BenchmarkOptimized(b *testing.B) {
	cd := New()
	var x int

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ptr := unsafe.Pointer(&x)
		val := reflect.ValueOf(&x)
		cd.Track(uintptr(ptr), reflect.TypeOf(x), val)
		cd.Check(uintptr(ptr), reflect.TypeOf(x))
	}
}

func BenchmarkCycleDetector(b *testing.B) {
	cd := New()
	var x int

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ptr := unsafe.Pointer(&x)
		val := reflect.ValueOf(&x)
		cd.Track(uintptr(ptr), reflect.TypeOf(x), val)
		cd.Check(uintptr(ptr), reflect.TypeOf(x))
	}
}
