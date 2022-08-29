package core

import "testing"

type A interface {
	Theme(string)
}

type Abc struct{}

func (Abc) Theme(x string) {

}
func Test_abc(t *testing.T) {
	var x A = new(Abc)
	t.Errorf("%T", x)
}
