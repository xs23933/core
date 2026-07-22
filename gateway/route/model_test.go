package route

import (
	"reflect"
	"testing"
)

func TestDefinitionDoesNotExposeParameters(t *testing.T) {
	if _, exists := reflect.TypeOf(Definition{}).FieldByName("Parameters"); exists {
		t.Fatal("Definition still exposes Parameters")
	}
}

func TestCatalogContainsOnlyServiceAndRoutes(t *testing.T) {
	typeOfCatalog := reflect.TypeOf(Catalog{})
	if typeOfCatalog.NumField() != 2 {
		t.Fatalf("Catalog fields = %d, want 2", typeOfCatalog.NumField())
	}
	if field := typeOfCatalog.Field(0); field.Name != "ServiceName" || field.Tag.Get("json") != "service_name" {
		t.Fatalf("Catalog field 0 = %#v", field)
	}
	if field := typeOfCatalog.Field(1); field.Name != "Routes" || field.Tag.Get("json") != "routes" {
		t.Fatalf("Catalog field 1 = %#v", field)
	}
}
