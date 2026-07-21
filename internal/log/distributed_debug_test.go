package log

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/require"
)

func TestLogStoreDebug(t *testing.T) {
	dir, _ := ioutil.TempDir("", "test")
	defer os.RemoveAll(dir)

	c := Config{}
	c.Segment.InitialOffset = 1
	c.Segment.MaxStoreBytes = 1024
	c.Segment.MaxIndexBytes = 1024

	ls, err := newLogStore(dir, c)
	require.NoError(t, err)

	first, _ := ls.FirstIndex()
	last, _ := ls.LastIndex()
	t.Logf("FirstIndex: %d, LastIndex: %d", first, last)

	err = ls.StoreLogs([]*raft.Log{{
		Index: 1,
		Term:  1,
		Type:  raft.LogCommand,
		Data:  []byte("hello"),
	}})
	t.Logf("StoreLogs err: %v", err)
	require.NoError(t, err)
}
