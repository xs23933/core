package core

import (
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"
)

func TestMoneyValueReturnsDecimalString(t *testing.T) {
	var valuer driver.Valuer = Money(12.345)

	value, err := valuer.Value()
	if err != nil {
		t.Fatalf("Money.Value() error = %v", err)
	}
	if got, ok := value.(string); !ok || got != "12.345" {
		t.Fatalf("Money.Value() = %#v (%T), want decimal string %q", value, value, "12.345")
	}
}

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
