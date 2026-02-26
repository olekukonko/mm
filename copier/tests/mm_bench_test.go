package tests

import (
	"testing"

	"github.com/olekukonko/mm/copier"
)

// Benchmarks (Keep as is, verify they run)
func BenchmarkSimpleCopy(b *testing.B) {
	src := SimpleSrc{Name: "Benchmark", Age: 100}
	var dest SimpleDest
	c := copier.NewCopier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}
func BenchmarkNestedCopy(b *testing.B) {
	src := NestedSrc{ID: 999, Child: SimpleSrc{Name: "NestedBench", Age: 99}}
	var dest NestedDest
	c := copier.NewCopier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}
func BenchmarkSliceCopy_Simple(b *testing.B) {
	src := SliceSrc{Ints: make([]int, 100)}
	for i := 0; i < 100; i++ {
		src.Ints[i] = i
	}
	var dest SliceDest
	c := copier.NewCopier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}
func BenchmarkSliceCopy_Complex(b *testing.B) {
	src := SliceSrc{Structs: make([]SimpleSrc, 50)}
	for i := 0; i < 50; i++ {
		src.Structs[i] = SimpleSrc{Name: "Bench", Age: i}
	}
	var dest SliceDest
	c := copier.NewCopier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}
func BenchmarkCycleCopy(b *testing.B) {
	node1 := &Cyclic{Value: 1}
	node2 := &Cyclic{Value: 2}
	node1.Self = node2
	node2.Self = node1
	var dest Cyclic
	c := copier.NewCopier().WithCycleDetection(true)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, node1)
	}
}
func BenchmarkUnsafeSimpleCopy(b *testing.B) {
	// Use a type suitable for unsafe (no pointers/slices/maps/etc)
	type BenchUnsafeType struct {
		Age  int64
		Val  float64
		Flag bool
		ID   uint32
	}
	src := BenchUnsafeType{Age: 100, Val: 1.23, Flag: true, ID: 99}
	var dest BenchUnsafeType
	c := copier.NewCopier().WithUnsafe(true).WithUnexported(false) // Unsafe should optimize basic copies
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}
func BenchmarkReflectSimpleCopy(b *testing.B) {
	// Use the same simple type as unsafe bench
	type BenchUnsafeType struct {
		Age  int64
		Val  float64
		Flag bool
		ID   uint32
	}
	src := BenchUnsafeType{Age: 100, Val: 1.23, Flag: true, ID: 99}
	var dest BenchUnsafeType
	c := copier.NewCopier().WithUnsafe(false) // Force reflection path
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}
func BenchmarkMapToStruct(b *testing.B) {
	srcMap := map[string]interface{}{"ID": 456, "DestName": "Mapped Name", "Email": "map@example.com", "ExtraKey": "Some Value"}
	var dest DestWithUnmapped
	c := copier.NewCopier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, srcMap)
	}
}
func BenchmarkStructToMap(b *testing.B) {
	src := SourceWithExtras{ID: 123, Name: "Source Name", Email: "test@example.com", Address: "123 Main St"}
	dest := make(map[string]interface{})
	c := copier.NewCopier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Re-initialize dest map inside loop? Or assume map clear is fast enough?
		// For micro-benchmark, maybe initialize inside:
		// dest = make(map[string]interface{})
		_ = c.Copy(&dest, src)
	}
}
