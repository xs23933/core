package core

import (
	"reflect"
	"testing"
)

func TestNSQConsumerConnectionTargetsPreferLookupd(t *testing.T) {
	lookupds, nsqds, err := nsqConsumerConnectionTargets(NSQConsumerConfig{
		Lookupds: []string{"192.168.31.5:4161"},
		NSQDs:    []string{"192.168.31.5:4150"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(lookupds, []string{"192.168.31.5:4161"}) {
		t.Fatalf("lookupds = %v, want lookupd only", lookupds)
	}
	if len(nsqds) != 0 {
		t.Fatalf("nsqds = %v, want empty when lookupd is configured", nsqds)
	}
}

func TestNSQConsumerConnectionTargetsUseNSQDWithoutLookupd(t *testing.T) {
	lookupds, nsqds, err := nsqConsumerConnectionTargets(NSQConsumerConfig{
		NSQDs: []string{"192.168.31.5:4150"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lookupds) != 0 {
		t.Fatalf("lookupds = %v, want empty", lookupds)
	}
	if !reflect.DeepEqual(nsqds, []string{"192.168.31.5:4150"}) {
		t.Fatalf("nsqds = %v, want configured nsqd", nsqds)
	}
}

func TestNSQConsumerConnectionTargetsRequireAddress(t *testing.T) {
	if _, _, err := nsqConsumerConnectionTargets(NSQConsumerConfig{}); err == nil {
		t.Fatal("expected missing address error")
	}
}

func TestNSQConsumerBuilderLookupdSuppressesDirectNSQD(t *testing.T) {
	consumer, err := NewNSQConsumerBuilder("topic", "channel").
		WithLookupd("192.168.31.5:4161").
		WithNSQD("192.168.31.5:4150").
		Build()
	if err != nil {
		t.Fatalf("build consumer: %v", err)
	}
	if len(consumer.config.NSQDs) != 0 {
		t.Fatalf("builder nsqds = %v, want empty when lookupd is configured", consumer.config.NSQDs)
	}
	if !reflect.DeepEqual(consumer.config.Lookupds, []string{"192.168.31.5:4161"}) {
		t.Fatalf("builder lookupds = %v, want configured lookupd", consumer.config.Lookupds)
	}
}

func TestNSQConsumerBuilderLookupdClearsEarlierDirectNSQD(t *testing.T) {
	consumer, err := NewNSQConsumerBuilder("topic", "channel").
		WithNSQD("192.168.31.5:4150").
		WithLookupd("192.168.31.5:4161").
		Build()
	if err != nil {
		t.Fatalf("build consumer: %v", err)
	}
	if len(consumer.config.NSQDs) != 0 {
		t.Fatalf("builder nsqds = %v, want empty when lookupd is configured", consumer.config.NSQDs)
	}
	if !reflect.DeepEqual(consumer.config.Lookupds, []string{"192.168.31.5:4161"}) {
		t.Fatalf("builder lookupds = %v, want configured lookupd", consumer.config.Lookupds)
	}
}
