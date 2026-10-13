package core

import "testing"

// TestNSQMaxAttemptsPolicy 验证默认兼容及持久化业务显式关闭超限自动确认。
func TestNSQMaxAttemptsPolicy(t *testing.T) {
	for _, test := range []struct {
		name    string
		limit   uint16
		disable bool
		want    uint16
	}{
		{name: "default", want: 5},
		{name: "explicit finite", limit: 2, want: 2},
		{name: "unlimited", disable: true, want: 0},
		{name: "unlimited overrides finite", limit: 2, disable: true, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			consumer, err := NewNSQConsumer(NSQConsumerConfig{Topic: "policy", Channel: "policy", MaxAttempts: test.limit, DisableMaxAttempts: test.disable})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(consumer.Stop)
			if consumer.config.MaxAttempts != test.want {
				t.Fatalf("effective attempts=%d want=%d", consumer.config.MaxAttempts, test.want)
			}
		})
	}
}
