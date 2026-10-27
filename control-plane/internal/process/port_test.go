package process

import (
	"net"
	"testing"
)

func TestAllocateSkipsAPortSomethingElseHolds(t *testing.T) {
	l, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	taken := l.Addr().(*net.TCPAddr).Port

	pa := newPortAllocator(taken)
	got := pa.Allocate()
	if got == taken {
		t.Fatalf("handed out port %d, which another process is listening on", taken)
	}
	if again := pa.Allocate(); again == got || again == taken {
		t.Errorf("ports repeated or reused a held one: %d, %d (held %d)", got, again, taken)
	}
}
