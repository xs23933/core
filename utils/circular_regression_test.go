package utils

import (
	"reflect"
	"testing"
)

func TestCircularInsertionOrder(t *testing.T) {
	m := NewInt64CircularMapBuffer[int](3)
	a := NewCircularBuffer[int](3)
	check := func(want ...int) {
		t.Helper()
		if !reflect.DeepEqual(m.Values(), want) || !reflect.DeepEqual(a.Values(), want) {
			t.Fatalf("map=%v array=%v want=%v", m.Values(), a.Values(), want)
		}
	}
	m.Save(1, 1)
	a.Save(1)
	check(1)
	m.Save(2, 2)
	a.Save(2)
	check(1, 2)
	m.Save(3, 3)
	a.Save(3)
	check(1, 2, 3)
	m.Save(4, 4)
	a.Save(4)
	check(2, 3, 4)
	m.Save(3, 30)
	if a.Save(3) {
		t.Fatal("duplicate")
	}
	if !reflect.DeepEqual(m.Values(), []int{2, 30, 4}) {
		t.Fatal(m.Values())
	}
	m.Save(3, 3)
	m.Delete(3)
	a.Delete(3)
	check(2, 4)
	m.Save(5, 5)
	a.Save(5)
	check(2, 4, 5)
	m.Delete(2)
	a.Delete(2)
	check(4, 5)
	m.Delete(5)
	a.Delete(5)
	check(4)
	m.Delete(99)
	a.Delete(99)
	check(4)
	m.Save(6, 6)
	a.Save(6)
	check(4, 6)
	if a.Contains(2) || a.Contains(3) || !a.Contains(4) || !a.Contains(6) {
		t.Fatal("index map")
	}
	k, v, ok := m.Find(func(_ int64, _ int) bool { return true })
	if !ok || k != 4 || v != 4 {
		t.Fatal(k, v, ok)
	}
	s := NewStringCircularMapBuffer[int](3)
	s.Save("a", 10)
	if !reflect.DeepEqual(s.Values(), []int{10}) {
		t.Fatal(s.Values())
	}
}

func TestCircularArrayDelete(t *testing.T) {
	for _, deleted := range []int{2, 3, 4} {
		b := NewCircularBuffer[int](3)
		for _, v := range []int{1, 2, 3, 4} {
			b.Save(v)
		}
		b.Delete(deleted)
		want := []int{}
		for _, v := range []int{2, 3, 4} {
			if v != deleted {
				want = append(want, v)
			}
		}
		if !reflect.DeepEqual(b.Values(), want) || b.Contains(deleted) {
			t.Fatalf("delete %d: %v want %v", deleted, b.Values(), want)
		}
		b.Save(5)
		want = append(want, 5)
		if !reflect.DeepEqual(b.Values(), want) {
			t.Fatal(b.Values(), want)
		}
		b.Save(6)
		want = append(want[1:], 6)
		if !reflect.DeepEqual(b.Values(), want) {
			t.Fatal(b.Values(), want)
		}
	}
}
