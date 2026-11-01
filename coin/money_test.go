package coin

import "testing"

func TestFixedPointArithmetic(t *testing.T) {
	for _, tt := range []struct{ a, b, mul, div string }{
		{"2", "3", "6", "0.66666666"}, {"-2", "3", "-6", "-0.66666666"},
		{"2", "-3", "-6", "-0.66666666"}, {"0.00000001", "0.5", "0", "0.00000002"},
	} {
		a, b := MustParse(tt.a), MustParse(tt.b)
		if got := a.Mul(b).StringFull(); got != tt.mul {
			t.Errorf("Mul = %s, want %s", got, tt.mul)
		}
		if got := a.Div(b).StringFull(); got != tt.div {
			t.Errorf("Div = %s, want %s", got, tt.div)
		}
		if a.StringFull() != tt.a || b.StringFull() != tt.b {
			t.Fatal("operand mutated")
		}
	}
	var z Money
	if !z.Mul(One()).IsZero() || !z.Div(One()).IsZero() || !One().Sub(z).Greater(z) || z.StringFull() != "0" {
		t.Fatal("zero value")
	}
	t.Run("divide zero", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("expected divide by zero panic")
			}
		}()
		One().Div(z)
	})
}

func TestSQLValueIgnoresDisplayPrecision(t *testing.T) {
	old := GetDefaultOutputDecimals()
	defer SetDefaultOutputDecimals(old)
	for _, decimals := range []int{0, 2, 8} {
		SetDefaultOutputDecimals(decimals)
		for _, s := range []string{"1.23456789", "-1.23456789", "0"} {
			m := MustParse(s)
			v, err := m.Value()
			if err != nil || v != s {
				t.Fatalf("Value=%v, %v", v, err)
			}
			var out Money
			if err := out.Scan(v); err != nil || out.Cmp(m) != 0 {
				t.Fatalf("Scan=%v", err)
			}
		}
	}
}

func TestBigAmountAndSignedSubunitArithmetic(t *testing.T) {
	for _, text := range []string{"999999999999999999999999999999.12345678", "-999999999999999999999999999999.12345678", "0.00000001", "-0.00000001"} {
		m := MustParse(text)
		if got := m.Mul(One()).StringFull(); got != text {
			t.Fatalf("multiply identity=%s want=%s", got, text)
		}
		if got := m.Div(One()).StringFull(); got != text {
			t.Fatalf("divide identity=%s want=%s", got, text)
		}
	}
	for _, text := range []string{"0.00000001", "-0.00000001"} {
		m := MustParse(text)
		if !m.Mul(MustParse("0.5")).IsZero() || !m.Div(MustParse("2")).IsZero() {
			t.Fatalf("subunit result must truncate toward zero: %s", text)
		}
	}
}
