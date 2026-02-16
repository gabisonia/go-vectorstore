package vectordata

import "testing"

func TestNormalizeFieldRef_MetadataTrimmed(t *testing.T) {
	normalized, err := NormalizeFieldRef(Metadata("  flags  ", " pinned "))
	if err != nil {
		t.Fatalf("NormalizeFieldRef: %v", err)
	}
	if len(normalized.Path) != 2 || normalized.Path[0] != "flags" || normalized.Path[1] != "pinned" {
		t.Fatalf("unexpected normalized path: %#v", normalized.Path)
	}
}

func TestNormalizeFieldRef_ColumnRejectsEmpty(t *testing.T) {
	_, err := NormalizeFieldRef(Column("   "))
	if err == nil {
		t.Fatal("expected error for empty column")
	}
}
