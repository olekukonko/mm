package tests

import (
	"github.com/olekukonko/mm"
	"github.com/olekukonko/mm/copier"
	"testing"
)

// Cyclic struct for testing cycle detection
type Cyclic struct {
	Value int
	Self  *Cyclic
	Data  interface{} // For interface-based cycles
}

func TestCycleDetection(t *testing.T) {
	// Setup: node1 -> node2 -> node1 (Self cycle)
	//        node1.Data -> node2 (Interface cycle)
	node1 := &Cyclic{Value: 1}
	node2 := &Cyclic{Value: 2}
	node1.Self = node2
	node2.Self = node1
	node1.Data = node2

	c := copier.NewCopier().WithCycleDetection(true).WithDebug(true) // Debug typically off for tests unless actively debugging

	// Part 1: Test with copier.Copy (destination is a pointer, &dest1)
	var dest1 Cyclic             // dest1 is a value
	err := c.Copy(&dest1, node1) // Pass address of dest1
	if err != nil {
		t.Fatalf("Cycle detection Copy failed: %v", err)
	}

	// Assertions for copier.Copy
	if dest1.Value != 1 {
		t.Errorf("Copy: dest1.Value mismatch: got %d, expected 1", dest1.Value)
	}
	if dest1.Self == nil {
		t.Fatalf("Copy: dest1.Self is nil")
	}
	if dest1.Self.Value != 2 {
		t.Errorf("Copy: dest1.Self.Value mismatch: got %d, expected 2", dest1.Self.Value)
	}
	if dest1.Self.Self == nil {
		t.Fatalf("Copy: dest1.Self.Self is nil")
	}
	// For copier.Copy(&dest1, ...), pointers inside dest1 should point to &dest1
	if dest1.Self.Self != &dest1 {
		t.Errorf("Copy: Cycle failed: dest1.Self.Self (%p) != &dest1 (%p)", dest1.Self.Self, &dest1)
	}

	// Check interface cycle for copier.Copy
	if dest1.Data == nil {
		t.Fatalf("Copy: dest1.Data is nil")
	}
	destDataPtr, ok := dest1.Data.(*Cyclic)
	if !ok {
		t.Fatalf("Copy: dest1.Data is not *Cyclic, type is %T, value is %+v", dest1.Data, dest1.Data)
	}
	if destDataPtr != dest1.Self { // Data should point to the same instance as Self
		t.Errorf("Copy: Cycle via interface failed: dest1.Data (%p) != dest1.Self (%p)", destDataPtr, dest1.Self)
	}

	// Part 2: Test Generic mm.CopyWith[Cyclic] (destination is a value type T = Cyclic)
	// c.WithDebug(true) // Optionally enable debug for this specific call if needed
	genDest, errGen := mm.CopyWith[Cyclic](c, node1) // T is Cyclic (value type)
	if errGen != nil {
		t.Fatalf("Cycle detection generic CopyWith failed: %v", errGen)
	}

	// Assertions for mm.CopyWith[Cyclic]
	if genDest.Value != 1 {
		t.Errorf("CopyWith: genDest.Value mismatch: got %d, expected 1", genDest.Value)
	}
	if genDest.Self == nil {
		t.Fatalf("CopyWith: genDest.Self is nil")
	}
	if genDest.Self.Value != 2 {
		t.Errorf("CopyWith: genDest.Self.Value mismatch: got %d, expected 2", genDest.Self.Value)
	}
	if genDest.Self.Self == nil {
		t.Fatalf("CopyWith: genDest.Self.Self is nil")
	}

	// MODIFIED ASSERTION for mm.CopyWith[Cyclic]:
	// genDest.Self.Self will point to the *internal* "root" of the cycle that was copied.
	// This internal root is what genDest.Self also points to.
	// It will NOT be equal to &genDest (address of the returned copy).
	if genDest.Self.Self != genDest.Self {
		t.Errorf("CopyWith: Generic cycle internal consistency failed: genDest.Self.Self (%p) != genDest.Self (%p)", genDest.Self.Self, genDest.Self)
	}
	// Additionally, verify that the cycle is not just two nil pointers pointing to each other (already covered by nil checks above)
	// and that the values are consistent if they point to the same conceptual node.
	if genDest.Self.Self.Value != genDest.Value {
		t.Errorf("CopyWith: Value mismatch in deep cycle: genDest.Self.Self.Value (%d) != genDest.Value (%d)", genDest.Self.Self.Value, genDest.Value)
	}

	// Check interface cycle within the generic structure for mm.CopyWith[Cyclic]
	if genDest.Data == nil {
		t.Fatalf("CopyWith: genDest.Data is nil")
	}
	genDestDataPtr, ok := genDest.Data.(*Cyclic)
	if !ok {
		t.Fatalf("CopyWith: genDest.Data is not *Cyclic, type is %T, value is %+v", genDest.Data, genDest.Data)
	}
	// The Data field should also point to the same internal cyclic node as genDest.Self
	if genDestDataPtr != genDest.Self {
		t.Errorf("CopyWith: Generic cycle via interface failed: genDest.Data (%p) != genDest.Self (%p)", genDestDataPtr, genDest.Self)
	}

	// Optional: Test with mm.CopyWith[*Cyclic] to show pointer identity works as expected when T is a pointer.
	genDestPtr, errGenPtr := mm.CopyWith[*Cyclic](c, node1)
	if errGenPtr != nil {
		t.Fatalf("Cycle detection generic CopyWith[*Cyclic] failed: %v", errGenPtr)
	}
	if genDestPtr.Self.Self != genDestPtr {
		t.Errorf("CopyWith[*Cyclic]: Cycle failed: genDestPtr.Self.Self (%p) != genDestPtr (%p)", genDestPtr.Self.Self, genDestPtr)
	}
	if genDestPtr.Data.(*Cyclic) != genDestPtr.Self {
		t.Errorf("CopyWith[*Cyclic]: Interface cycle failed: genDestPtr.Data (%p) != genDestPtr.Self (%p)", genDestPtr.Data.(*Cyclic), genDestPtr.Self)
	}
}
