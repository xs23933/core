package gen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGeneratedEnumBoundaries 编译真实生成产物，验证驱动输入及共享工具边界。
func TestGeneratedEnumBoundaries(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateEnums(EnumConfig{Package: "fixture", Output: dir, Enums: []Enum{{Name: "State", Values: []string{"none", "active", "disabled"}}}}); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(dir, "scan_test.go")
	if err := os.WriteFile(testFile, []byte(generatedScanTests), 0600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", filepath.Join(dir, "gen_state.go"), testFile, "-count=1")
	cmd.Dir = filepath.Dir(cwd)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated enum regression: %v\n%s", err, output)
	}
}

const generatedScanTests = `package fixture
import (
 "github.com/xs23933/core/v3"
 "math"
 "testing"
)
func TestDriverTypes(t *testing.T) {
 for _, input := range []any{int64(1), uint8(1), uint16(1), uint32(1), float64(1), "active", []byte("active")} {
  state := STATE_DISABLED
  if err := state.Scan(input); err != nil || state != STATE_ACTIVE { t.Errorf("Scan(%T) = %v/%v", input, state, err) }
 }
}
func TestRejectLossyNumbersWithoutMutation(t *testing.T) {
 for _, tc := range []struct{name string; input any}{
  {"negative",int64(-1)}, {"int64_overflow",int64(257)}, {"uint16_overflow",uint16(257)},
  {"uint32_overflow",uint32(257)}, {"fraction",1.5}, {"float_negative",-1.0},
  {"float_overflow",257.0}, {"nan",math.NaN()}, {"infinity",math.Inf(1)},
 } {
  t.Run(tc.name,func(t *testing.T){ state:=STATE_DISABLED; err:=state.Scan(tc.input)
   if err==nil || state!=STATE_DISABLED {t.Errorf("Scan(%v) = %d/%v; want error and unchanged 2",tc.input,state,err)}
  })
 }
}
func TestUnsupportedInputDoesNotMutate(t *testing.T) {
 for _, input := range []any{nil, true, struct{}{}} { state:=STATE_DISABLED
  if err:=state.Scan(input);err==nil || state!=STATE_DISABLED {t.Errorf("Scan(%T) = %d/%v",input,state,err)}
 }
}
func TestJSONRejectsWrappedNumbers(t *testing.T) {
 for _, raw:=range []string{"257","-255","1.5","1e30"} {state:=STATE_DISABLED
  if err:=state.UnmarshalJSON([]byte(raw));err==nil || state!=STATE_DISABLED {t.Errorf("JSON(%s) = %d/%v",raw,state,err)}
 }
 mapping:=make([]string,256);mapping[255]="last"
 if value,err:=core.EnumUnmarshalJSON[State]([]byte("255"),mapping);err!=nil || value!=255 {t.Errorf("full uint8 mapping = %d/%v",value,err)}
}
func TestInvalidEnumFormattingDoesNotPanic(t *testing.T) {
 for _, method:=range []string{"String","MarshalJSON","MarshalText"} {t.Run(method,func(t *testing.T){
  defer func(){if p:=recover();p!=nil{t.Errorf("invalid enum panicked: %v",p)}}()
  state:=State(255)
  switch method {
  case "String":if got:=state.String();got!=""{t.Errorf("String=%q",got)}
  case "MarshalJSON":if _,err:=state.MarshalJSON();err==nil{t.Error("missing error")}
  case "MarshalText":if _,err:=core.EnumMarshalText(state,StateMapping);err==nil{t.Error("missing error")}
  }
 })}
}
`

// TestDottedProtocolValues 使用真实协议值生成可编译的常量。
func TestDottedProtocolValues(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateEnums(EnumConfig{Package: "fixture", Output: dir, Enums: []Enum{{Name: "Event", Values: []string{"none", "transaction.payment.v1", "zh-CN"}}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "event_test.go")
	source := `package fixture;import "testing";func TestWire(t *testing.T){if EVENT_TRANSACTION_PAYMENT_V1.String()!="transaction.payment.v1"||EVENT_ZH_CN.String()!="zh-CN"{t.Fatal("wire values changed")}}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", filepath.Join(dir, "gen_event.go"), path, "-count=1")
	cmd.Dir = filepath.Dir(cwd)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dotted enum regression: %v\n%s", err, output)
	}
}
