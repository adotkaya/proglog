package log

import (
	"io/ioutil"
	"os"
	"testing"

	api "github.com/adotkaya/proglog/api/v1"
)

func TestStoreLogsDebug(t *testing.T) {
	dir, _ := ioutil.TempDir("", "test")
	defer os.RemoveAll(dir)

	c := Config{}
	c.Segment.InitialOffset = 1
	c.Segment.MaxStoreBytes = 1024
	c.Segment.MaxIndexBytes = 1024

	l, err := NewLog(dir, c)
	if err != nil {
		t.Fatal("NewLog error:", err)
	}

	ho, _ := l.HighestOffset()
	lo, _ := l.LowestOffset()
	t.Logf("HighestOffset: %d, LowestOffset: %d", ho, lo)

	record := &api.Record{Value: []byte("test"), Term: 1, Type: 1}
	off, err := l.Append(record)
	t.Logf("Append result: off=%d, err=%v", off, err)
	if err != nil {
		t.Fatal(err)
	}
}
