package core

import (
	"reflect"
	"strings"
	"testing"
)

func TestSModelsUsesTimestamp6(t *testing.T) {
	typ := reflect.TypeOf(SModels{})
	for _, name := range []string{"CreatedAt", "UpdatedAt", "DeletedAt"} {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("field %s missing", name)
		}
		if got := field.Tag.Get("gorm"); !strings.Contains(got, "type:timestamp(6)") {
			t.Fatalf("%s gorm tag = %q, want type:timestamp(6)", name, got)
		}
	}
}
