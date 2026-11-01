package coins

import (
	"math"
	"testing"
)

func TestRatioWideIntermediate(t *testing.T) {
	if got := MustParse("1000").MulRate(MoneyBase).String(); got != "1000" {
		t.Fatal(got)
	}
	for _, tt := range []struct{ v, n, d, w int64 }{
		{math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxInt64},
		{math.MinInt64, 1, 1, math.MinInt64}, {math.MinInt64, -1, -1, math.MinInt64},
		{-5, 1, 2, -2}, {5, 1, -2, -2}, {5, 1, 0, 0}, {0, math.MaxInt64, 1, 0},
	} {
		got, err := FromInt64(tt.v).SafeMulRatio(tt.n, tt.d)
		if err != nil || got.Int64() != tt.w {
			t.Fatalf("%+v: %d %v", tt, got.Int64(), err)
		}
	}
	for _, v := range []int64{math.MaxInt64, math.MinInt64} {
		if _, err := FromInt64(v).SafeMulRatio(2, 1); err == nil {
			t.Fatal("overflow accepted")
		}
	}
	t.Run("legacy overflow", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("expected overflow panic")
			}
		}()
		FromInt64(math.MinInt64).MulRatio(-1, 1)
	})
}

func TestMinimumAmountRoundTrip(t *testing.T) {
	m, err := FromInt64(math.MinInt64).SafeMulRatio(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	const want = "-92233720368.54775808"
	if got := m.String(); got != want {
		t.Fatalf("minimum String=%q want=%q", got, want)
	}
	parsed, err := Parse(want)
	if err != nil || parsed.Int64() != math.MinInt64 {
		t.Fatalf("parse minimum=%d error=%v", parsed.Int64(), err)
	}
	value, err := m.Value()
	if err != nil || value != int64(math.MinInt64) {
		t.Fatalf("SQL value=%v error=%v", value, err)
	}
	var scanned Money
	if err := scanned.Scan(value); err != nil || scanned.Int64() != math.MinInt64 {
		t.Fatalf("SQL round trip=%d error=%v", scanned.Int64(), err)
	}
}

func TestSignedAmountLimits(t *testing.T) {
	for _, tc := range []struct {
		raw  int64
		text string
	}{
		{math.MinInt64, "-92233720368.54775808"}, {math.MaxInt64, "92233720368.54775807"}, {-1, "-0.00000001"}, {0, "0"}, {1, "0.00000001"},
	} {
		m, err := Parse(tc.text)
		if err != nil || m.Int64() != tc.raw {
			t.Fatalf("parse %q=%d error=%v", tc.text, m.Int64(), err)
		}
		if got := m.String(); got != tc.text {
			t.Fatalf("format=%q want=%q", got, tc.text)
		}
		if got := m.MulRate(MoneyBase); got.Int64() != tc.raw {
			t.Fatalf("identity rate=%d want=%d", got.Int64(), tc.raw)
		}
		data, err := m.MarshalJSONWithDecimals(8)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Money
		if err := decoded.UnmarshalJSON(data); err != nil || decoded.Int64() != tc.raw {
			t.Fatalf("JSON=%s value=%d error=%v", data, decoded.Int64(), err)
		}
	}
	for _, text := range []string{"92233720368.54775808", "-92233720368.54775809"} {
		if _, err := Parse(text); err == nil {
			t.Fatalf("accepted out of range %s", text)
		}
	}
	if got := FromInt64(math.MinInt64).StringFixed(2); got != "-92233720368.54" {
		t.Fatal(got)
	}
	for _, tc := range []struct{ v, n, d, w int64 }{
		{math.MaxInt64, math.MinInt64, math.MinInt64, math.MaxInt64},
		{math.MinInt64, math.MinInt64, math.MinInt64, math.MinInt64},
		{1, 1, math.MinInt64, 0}, {-1, 1, 2, 0},
	} {
		got, err := FromInt64(tc.v).SafeMulRatio(tc.n, tc.d)
		if err != nil || got.Int64() != tc.w {
			t.Fatalf("%+v = %d error=%v", tc, got.Int64(), err)
		}
	}
}
