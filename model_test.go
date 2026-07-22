package core

import (
	"reflect"
	"strings"
	"testing"
)

func TestSModelsDoesNotForceDialectSpecificTimestampType(t *testing.T) {
	typ := reflect.TypeOf(SModels{})
	for _, name := range []string{"CreatedAt", "UpdatedAt", "DeletedAt"} {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("field %s missing", name)
		}
		if got := field.Tag.Get("gorm"); strings.Contains(got, "type:") {
			t.Fatalf("%s gorm tag = %q, must not force a database-specific column type", name, got)
		}
	}
}
