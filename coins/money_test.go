package coins

import "testing"

func TestNewScalesIntegerUnits(t *testing.T) {
	m := New(100)

	if got := m.Int64(); got != 100*MoneyBase {
		t.Fatalf("New(100).Int64() = %d, want %d", got, 100*MoneyBase)
	}
}

func TestNewScalesDecimalUnits(t *testing.T) {
	tests := []struct {
		name string
		got  Money
		want int64
	}{
		{name: "twenty five point thirty two", got: New(25.32), want: 2532000000},
		{name: "one hundred point ten", got: New(100.10), want: 10010000000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.got.Int64(); got != tt.want {
				t.Fatalf("Int64() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNewScalesStringUnits(t *testing.T) {
	m := New("25.32")

	if got := m.Int64(); got != 2532000000 {
		t.Fatalf("New(%q).Int64() = %d, want 2532000000", "25.32", got)
	}
}

func TestNewScalesIntVariables(t *testing.T) {
	raw := 100
	m := New(raw)

	if got := m.Int64(); got != 100*MoneyBase {
		t.Fatalf("New(raw).Int64() = %d, want %d", got, 100*MoneyBase)
	}
}

func TestParseAcceptsStringIntAndFloatUnits(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  int64
	}{
		{name: "string", value: "25.32", want: 2532000000},
		{name: "int", value: 25, want: 25 * MoneyBase},
		{name: "float", value: 25.32, want: 2532000000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				m   Money
				err error
			)

			switch v := tt.value.(type) {
			case string:
				m, err = Parse(v)
			case int:
				m, err = Parse(v)
			case float64:
				m, err = Parse(v)
			default:
				t.Fatalf("unhandled test value type %T", v)
			}
			if err != nil {
				t.Fatalf("Parse(%v) returned error: %v", tt.value, err)
			}
			if got := m.Int64(); got != tt.want {
				t.Fatalf("Parse(%v).Int64() = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestMustParseAcceptsIntAndFloatUnits(t *testing.T) {
	tests := []struct {
		name string
		got  Money
		want int64
	}{
		{name: "int", got: MustParse(25), want: 25 * MoneyBase},
		{name: "float", got: MustParse(25.32), want: 2532000000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.got.Int64(); got != tt.want {
				t.Fatalf("Int64() = %d, want %d", got, tt.want)
			}
		})
	}
}
