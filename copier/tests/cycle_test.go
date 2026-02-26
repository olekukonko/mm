package tests

import (
	"testing"

	"github.com/olekukonko/mm"
	"github.com/olekukonko/mm/copier"
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

	// Note: Disabling debug for tests unless actively debugging one.
	c := copier.NewCopier().WithCycleDetection(true).WithDebug(false)

	// Part 1: Test with copier.Copy (destination is a pointer, &dest1)
	var dest1 Cyclic
	err := c.Copy(&dest1, node1)
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
	if dest1.Self.Self != &dest1 {
		t.Errorf("Copy: Cycle failed: dest1.Self.Self (%p) != &dest1 (%p)", dest1.Self.Self, &dest1)
	}

	if dest1.Data == nil {
		t.Fatalf("Copy: dest1.Data is nil")
	}
	destDataPtr, ok := dest1.Data.(*Cyclic)
	if !ok {
		t.Fatalf("Copy: dest1.Data is not *Cyclic, type is %T, value is %+v", dest1.Data, dest1.Data)
	}
	if destDataPtr != dest1.Self {
		t.Errorf("Copy: Cycle via interface failed: dest1.Data (%p) != dest1.Self (%p)", destDataPtr, dest1.Self)
	}

	// Part 2: Test Generic mm.CopyWith[Cyclic] (destination is a value type T = Cyclic)
	genDest, errGen := mm.CopyWith[Cyclic](c, node1) // T is Cyclic (value type)
	if errGen != nil {
		t.Fatalf("Cycle detection generic CopyWith[Cyclic] failed: %v", errGen)
	}

	if genDest.Value != 1 {
		t.Errorf("CopyWith[Cyclic]: genDest.Value mismatch: got %d, expected 1", genDest.Value)
	}
	if genDest.Self == nil {
		t.Fatalf("CopyWith[Cyclic]: genDest.Self is nil")
	}
	if genDest.Self.Value != 2 {
		t.Errorf("CopyWith[Cyclic]: genDest.Self.Value mismatch: got %d, expected 2", genDest.Self.Value)
	}
	if genDest.Self.Self == nil {
		t.Fatalf("CopyWith[Cyclic]: genDest.Self.Self is nil")
	}
	// For CopyWith[Cyclic], genDest is the copied struct value.
	// genDest.Self.Self should point to a struct whose content matches genDest.
	// We cannot compare its address to &genDest because genDest is a returned value (a copy).
	// The crucial part is that genDest.Self.Self.Value == genDest.Value.
	if genDest.Self.Self.Value != genDest.Value {
		t.Errorf("CopyWith[Cyclic]: Value mismatch in deep cycle: genDest.Self.Self.Value (%d) != genDest.Value (%d). Ptrs: Self.Self %p, Self %p",
			genDest.Self.Self.Value, genDest.Value, genDest.Self.Self, genDest.Self)
	}
	// Further check: genDest.Self.Self and genDest.Self should be distinct pointers *unless* node1 == node2, which isn't the case.
	// But if node1.Self.Self was indeed node1, then genDest.Self.Self should be the conceptual "genDest".
	// The issue with `genDest.Self.Self (0x...) != genDest.Self (0x...)` indicates the internal pointers were distinct copies,
	// and genDest.Self.Self correctly pointed back to the initial copied object (represented by its pointer in the detector).
	// So, the previous assertion `genDest.Self.Self != genDest.Self` was actually correct for this structure.
	// The log `0x1400009e1e0 != 0x1400009e200` meant genDest.Self.Self (the copy of node1) is not genDest.Self (the copy of node2).
	// The problem is the test expected them to be the same, which is wrong.
	// Let's ensure genDest.Self.Self points to a struct with Value 1.
	if genDest.Self.Self.Value != 1 {
		t.Errorf("CopyWith[Cyclic]: genDest.Self.Self.Value should be 1 (node1's value), got %d", genDest.Self.Self.Value)
	}

	if genDest.Data == nil {
		t.Fatalf("CopyWith[Cyclic]: genDest.Data is nil")
	}
	genDestDataPtr, ok := genDest.Data.(*Cyclic)
	if !ok {
		t.Fatalf("CopyWith[Cyclic]: genDest.Data is not *Cyclic, type is %T, value is %+v", genDest.Data, genDest.Data)
	}
	// genDest.Data (copy of node2) should be the same instance as genDest.Self (copy of node2)
	if genDestDataPtr != genDest.Self {
		t.Errorf("CopyWith[Cyclic]: Cycle via interface failed: genDest.Data (%p) != genDest.Self (%p)", genDestDataPtr, genDest.Self)
	}

	// Part 3: Test Generic mm.CopyWith[*Cyclic]
	genDestPtr, errGenPtr := mm.CopyWith[*Cyclic](c, node1)
	if errGenPtr != nil {
		// This is where the "not settable for reassignment" error occurs.
		t.Fatalf("Cycle detection generic CopyWith[*Cyclic] failed: %v", errGenPtr)
	}
	if genDestPtr == nil {
		t.Fatalf("CopyWith[*Cyclic]: genDestPtr is nil")
	}
	if genDestPtr.Value != 1 {
		t.Errorf("CopyWith[*Cyclic]: genDestPtr.Value mismatch: got %d, expected 1", genDestPtr.Value)
	}
	if genDestPtr.Self == nil {
		t.Fatalf("CopyWith[*Cyclic]: genDestPtr.Self is nil")
	}
	if genDestPtr.Self.Value != 2 {
		t.Errorf("CopyWith[*Cyclic]: genDestPtr.Self.Value mismatch: got %d, expected 2", genDestPtr.Self.Value)
	}
	if genDestPtr.Self.Self == nil {
		t.Fatalf("CopyWith[*Cyclic]: genDestPtr.Self.Self is nil")
	}
	// For CopyWith[*Cyclic], genDestPtr is the pointer to the copied node1.
	// So, genDestPtr.Self.Self should be genDestPtr itself.
	if genDestPtr.Self.Self != genDestPtr {
		t.Errorf("CopyWith[*Cyclic]: Cycle failed: genDestPtr.Self.Self (%p) != genDestPtr (%p)", genDestPtr.Self.Self, genDestPtr)
	}

	if genDestPtr.Data == nil {
		t.Fatalf("CopyWith[*Cyclic]: genDestPtr.Data is nil")
	}
	genDestDataPtrForPtr, ok := genDestPtr.Data.(*Cyclic)
	if !ok {
		t.Fatalf("CopyWith[*Cyclic]: genDestPtr.Data is not *Cyclic, type is %T, value is %+v", genDestPtr.Data, genDestPtr.Data)
	}
	if genDestDataPtrForPtr != genDestPtr.Self {
		t.Errorf("CopyWith[*Cyclic]: Interface cycle failed: genDestPtr.Data (%p) != genDestPtr.Self (%p)", genDestDataPtrForPtr, genDestPtr.Self)
	}
}
