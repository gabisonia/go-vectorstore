package vectordata_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type normalizationFailure struct{ operation string }

func (e *normalizationFailure) Error() string { return "application " + e.operation + " failed" }

type failingUUID struct{ cause error }

func (u failingUUID) MarshalText() ([]byte, error) { return nil, u.cause }

type failingJSON struct{ cause error }

func (v failingJSON) MarshalJSON() ([]byte, error) { return nil, v.cause }

func TestNormalizationPreservesApplicationErrorCauses(t *testing.T) {
	cause := &normalizationFailure{operation: "marshal"}
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyUUID}, Data: []vectordata.DataProperty{{Name: "Owner", Type: vectordata.TypeUUID}, {Name: "Owners", Type: vectordata.TypeUUIDArray}, {Name: "Extra", Type: vectordata.TypeJSON}}}
	for _, tc := range []struct {
		name string
		run  func() error
		json bool
	}{
		{"key", func() error { _, err := d.NormalizeKey(failingUUID{cause}); return err }, false},
		{"uuid data", func() error { _, err := vectordata.NormalizeModelData(failingUUID{cause}, d.Data[0]); return err }, false},
		{"uuid array", func() error { _, err := vectordata.NormalizeModelData([]failingUUID{{cause}}, d.Data[1]); return err }, false},
		{"json data", func() error { _, err := vectordata.NormalizeModelData(failingJSON{cause}, d.Data[2]); return err }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			var got *normalizationFailure
			if !errors.Is(err, vectordata.ErrInvalidRecord) || !errors.Is(err, cause) || !errors.As(err, &got) || got != cause {
				t.Fatalf("lost normalization cause: %v", err)
			}
			if tc.json {
				var marshaler *json.MarshalerError
				if !errors.As(err, &marshaler) || !errors.Is(marshaler, cause) {
					t.Fatalf("lost JSON marshaler wrapper: %v", err)
				}
			}
		})
	}
	for _, filter := range []vectordata.Filter{
		vectordata.Eq(vectordata.Column("ID"), failingUUID{cause}),
		vectordata.In(vectordata.Column("Owner"), failingUUID{cause}),
		vectordata.Contains(vectordata.Column("Owners"), failingUUID{cause}),
		vectordata.Eq(vectordata.Column("Extra"), failingJSON{cause}),
		vectordata.Eq(vectordata.DataPath("Extra", "path"), failingJSON{cause}),
	} {
		err := vectordata.ValidateModelFilter(filter, d)
		var got *normalizationFailure
		if !errors.Is(err, vectordata.ErrInvalidFilter) || !errors.Is(err, cause) || !errors.As(err, &got) || got != cause {
			t.Fatalf("lost filter cause for %T: %v", filter, err)
		}
	}
}
