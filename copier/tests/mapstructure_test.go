// copier/tests/benchmark_mapstructure_test.go
package tests

import (
	"testing"

	"github.com/mitchellh/mapstructure"
	"github.com/olekukonko/mm/copier"
)

type MapstructureSrc struct {
	Name  string
	Age   int
	Email string
	Extra string
}

type MapstructureDest struct {
	Name  string
	Age   int
	Email string
	Extra string
}

func BenchmarkMapstructure_SimpleCopy(b *testing.B) {
	src := MapstructureSrc{Name: "Benchmark", Age: 100, Email: "test@example.com", Extra: "value"}
	var dest MapstructureDest
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mapstructure.Decode(src, &dest)
	}
}

func BenchmarkMMSimpleCopy(b *testing.B) {
	src := MapstructureSrc{Name: "Benchmark", Age: 100, Email: "test@example.com", Extra: "value"}
	var dest MapstructureDest
	c := copier.NewCopier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}

func BenchmarkMapstructure_NestedCopy(b *testing.B) {
	type NestedSrc struct {
		ID    int
		Child MapstructureSrc
	}
	type NestedDest struct {
		ID    int
		Child MapstructureDest
	}
	src := NestedSrc{ID: 999, Child: MapstructureSrc{Name: "NestedBench", Age: 99, Email: "nested@example.com"}}
	var dest NestedDest
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mapstructure.Decode(src, &dest)
	}
}

func BenchmarkMMNestedCopy(b *testing.B) {
	type NestedSrc struct {
		ID    int
		Child MapstructureSrc
	}
	type NestedDest struct {
		ID    int
		Child MapstructureDest
	}
	src := NestedSrc{ID: 999, Child: MapstructureSrc{Name: "NestedBench", Age: 99, Email: "nested@example.com"}}
	var dest NestedDest
	c := copier.NewCopier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}

func BenchmarkMapstructure_SliceCopy(b *testing.B) {
	src := struct {
		Items []MapstructureSrc
	}{Items: make([]MapstructureSrc, 50)}
	for i := 0; i < 50; i++ {
		src.Items[i] = MapstructureSrc{Name: "Bench", Age: i, Email: "item@example.com"}
	}
	var dest struct {
		Items []MapstructureDest
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mapstructure.Decode(src, &dest)
	}
}

func BenchmarkMMSliceCopy(b *testing.B) {
	src := struct {
		Items []MapstructureSrc
	}{Items: make([]MapstructureSrc, 50)}
	for i := 0; i < 50; i++ {
		src.Items[i] = MapstructureSrc{Name: "Bench", Age: i, Email: "item@example.com"}
	}
	var dest struct {
		Items []MapstructureDest
	}
	c := copier.NewCopier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}

func BenchmarkMapstructure_MapToStruct(b *testing.B) {
	srcMap := map[string]interface{}{
		"Name":  "Mapped Name",
		"Age":   456,
		"Email": "map@example.com",
		"Extra": "Some Value",
	}
	var dest MapstructureDest
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mapstructure.Decode(srcMap, &dest)
	}
}

func BenchmarkMMMapToStruct(b *testing.B) {
	srcMap := map[string]interface{}{
		"Name":  "Mapped Name",
		"Age":   456,
		"Email": "map@example.com",
		"Extra": "Some Value",
	}
	var dest MapstructureDest
	c := copier.NewCopier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, srcMap)
	}
}

func BenchmarkMapstructure_StructToMap(b *testing.B) {
	src := MapstructureSrc{Name: "Source Name", Age: 123, Email: "test@example.com", Extra: "123 Main St"}
	dest := make(map[string]interface{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mapstructure.Decode(src, &dest)
	}
}

func BenchmarkMMStructToMap(b *testing.B) {
	src := MapstructureSrc{Name: "Source Name", Age: 123, Email: "test@example.com", Extra: "123 Main St"}
	dest := make(map[string]interface{})
	c := copier.NewCopier()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Copy(&dest, src)
	}
}
