//go:build windows

package ipc

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func testPipe(t *testing.T) string {
	return fmt.Sprintf(`\\.\pipe\InternetDownloader-test-%d-%s`, os.Getpid(), t.Name())
}

func TestPingRoundTrip(t *testing.T) {
	name := testPipe(t)
	l, err := Listen(name)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go Serve(l, func(m Message) Message {
		return Message{Type: "pong", Payload: m.Payload}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 3; i++ {
		resp, err := c.Call(Message{Type: "ping", Payload: []byte(`{"n":1}`)})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Type != "pong" || string(resp.Payload) != `{"n":1}` || resp.ID != uint64(i+1) {
			t.Fatalf("unexpected response %+v", resp)
		}
	}
}

func TestSecondListenerFails(t *testing.T) {
	name := testPipe(t)
	l, err := Listen(name)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l2, err := Listen(name); err == nil {
		l2.Close()
		t.Fatal("second Listen on the same name should fail")
	}
}

func TestPipeNameUsesSID(t *testing.T) {
	n, err := PipeName()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(n, `\\.\pipe\InternetDownloader-S-1-`) {
		t.Fatalf("unexpected pipe name %q", n)
	}
}
