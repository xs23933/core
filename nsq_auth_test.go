package core

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nsqio/go-nsq"
)

// authNSQServer 使用真实 NSQ TCP 帧验证客户端认证，不连接外部服务。
func authNSQServer(t *testing.T, secret string) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	auths := make(chan string, 16)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				r := bufio.NewReader(conn)
				magic := make([]byte, 4)
				if _, err := io.ReadFull(r, magic); err != nil || string(magic) != "  V2" {
					return
				}
				writeFrame := func(kind int32, body string) {
					binary.Write(conn, binary.BigEndian, int32(len(body)+4))
					binary.Write(conn, binary.BigEndian, kind)
					io.WriteString(conn, body)
				}
				readBody := func() []byte {
					var n int32
					if binary.Read(r, binary.BigEndian, &n) != nil || n < 0 || n > 65536 {
						return nil
					}
					b := make([]byte, n)
					if _, err := io.ReadFull(r, b); err != nil {
						return nil
					}
					return b
				}
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case line == "IDENTIFY\n":
						var identify map[string]any
						if json.Unmarshal(readBody(), &identify) != nil || identify["feature_negotiation"] != true {
							return
						}
						writeFrame(0, `{"auth_required":true,"max_rdy_count":2500}`)
					case line == "AUTH\n":
						supplied := string(readBody())
						auths <- supplied
						if supplied != secret {
							writeFrame(1, "E_AUTH_FAILED")
							return
						}
						writeFrame(0, `{"identity":"fixture","permission_count":1}`)
					case line == "NOP\n":
					case line == "CLS\n":
						writeFrame(0, "CLOSE_WAIT")
						return
					case strings.HasPrefix(line, "SUB "):
						writeFrame(0, "OK")
					case strings.HasPrefix(line, "RDY "):
					default:
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); wg.Wait() })
	return ln.Addr().String(), auths
}

func TestNSQConsumerInheritsInitAuthentication(t *testing.T) {
	CloseNSQ()
	addr, auths := authNSQServer(t, "inherited-fixture-secret")
	if err := InitNSQ(Options{"nsqd": addr, "auth_secret": "inherited-fixture-secret"}, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseNSQ)
	if got := <-auths; got != "inherited-fixture-secret" {
		t.Fatal("producer AUTH mismatch")
	}
	consumer, err := NewNSQConsumer(NSQConsumerConfig{Topic: "auth_topic", Channel: "auth_channel", NSQDs: []string{addr}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(consumer.Stop)
	if err = consumer.StartAsync(func(*nsq.Message) error { return nil }); err != nil {
		t.Fatal(fmt.Errorf("consumer inherited AUTH: %w", err))
	}
	select {
	case got := <-auths:
		if got != "inherited-fixture-secret" {
			t.Fatal("consumer AUTH mismatch")
		}
	case <-time.After(time.Second):
		t.Fatal("consumer did not send AUTH")
	}
}

func TestNSQConsumerExplicitAuthenticationProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, secret string
		accepted     bool
	}{
		{"explicit", "fixture-secret", true},
		{"wrong", "wrong-fixture-secret", false},
		{"missing", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			CloseNSQ()
			addr, auths := authNSQServer(t, "fixture-secret")
			consumer, err := NewNSQConsumer(NSQConsumerConfig{Topic: "explicit_topic", Channel: "explicit_channel", NSQDs: []string{addr}, AuthSecret: tc.secret})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(CloseNSQ)
			t.Cleanup(consumer.Stop)
			err = consumer.StartAsync(func(*nsq.Message) error { return nil })
			if (err == nil) != tc.accepted {
				t.Fatalf("accepted=%v: %v", tc.accepted, err)
			}
			if tc.secret != "" {
				select {
				case got := <-auths:
					if got != tc.secret {
						t.Fatal("AUTH payload mismatch")
					}
				case <-time.After(time.Second):
					t.Fatal("missing AUTH command")
				}
			} else {
				select {
				case <-auths:
					t.Fatal("unexpected AUTH command without credentials")
				default:
				}
			}
		})
	}
}

func TestNSQConsumerExplicitOverridesAndCloseClearsDefault(t *testing.T) {
	CloseNSQ()
	producerAddr, _ := authNSQServer(t, "default-secret")
	if err := InitNSQ(Options{"nsqd": producerAddr, "auth_secret": "default-secret"}, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(CloseNSQ)
	consumerAddr, auths := authNSQServer(t, "override-secret")
	consumer, err := NewNSQConsumer(NSQConsumerConfig{Topic: "override_topic", Channel: "override_channel", NSQDs: []string{consumerAddr}, AuthSecret: "override-secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(consumer.Stop)
	if err = consumer.StartAsync(func(*nsq.Message) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if <-auths != "override-secret" {
		t.Fatal("explicit secret was not sent")
	}
	CloseNSQ()
	after, err := NewNSQConsumer(NSQConsumerConfig{Topic: "after_close", Channel: "after_close", NSQDs: []string{producerAddr}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(after.Stop)
	if err = after.StartAsync(func(*nsq.Message) error { return nil }); err == nil {
		t.Fatal("CloseNSQ retained default authentication")
	}
}
