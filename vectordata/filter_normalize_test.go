package vectordata

import "testing"

func TestNormalizeFieldRef_ColumnTrimmed(t *testing.T) {
	normalized, err := NormalizeFieldRef(Column("  id  "))
	if err != nil {
		t.Fatalf("NormalizeFieldRef: %v", err)
	}
	if normalized.Kind != FieldColumn || normalized.Name != "id" || normalized.Path != nil {
		t.Fatalf("unexpected normalized column: %#v", normalized)
	}
}

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

func TestNormalizeFieldRef_RejectsInvalidMetadataPath(t *testing.T) {
	tests := []struct {
		name string
		ref  FieldRef
	}{
		{name: "empty path", ref: Metadata()},
		{name: "blank segment", ref: Metadata("valid", "   ")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NormalizeFieldRef(tt.ref); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestNormalizeFieldRef_RejectsUnsupportedKind(t *testing.T) {
	_, err := NormalizeFieldRef(FieldRef{Kind: "unknown", Name: "id"})
	if err == nil {
		t.Fatal("expected error")
	}
}
