package log

import (
	"io"
	"os"

	"github.com/tysonmote/gommap"
)

var (
	offWidth uint64 = 4
	posWidth uint64 = 8
	entWidth uint64 = offWidth + posWidth
)

type index struct {
	file *os.File
	mmap gommap.MMap
	size uint64
}

func newIndex(file *os.File, c Config) (*index, error) {
	idx := &index{
		file: file,
	}
	fi, err := os.Stat(file.Name())
	if err != nil {
		return nil, err
	}
	idx.size = uint64(fi.Size())
	if err := os.Truncate(file.Name(), int64(c.Segment.MaxIndexBytes)); err != nil {
		return nil, err
	}
	if idx.mmap, err = gommap.Map(idx.file.Fd(), gommap.PROT_READ|gommap.PROT_WRITE, gommap.MAP_SHARED); err != nil {
		return nil, err
	}
	return idx, nil
}

// Close() makes sure the memory-mapped file has synced its data to the persisted file and that the persisted file has flushed its contents to stable storage.
// Then it truncates the persisted file to the amount of data that’s actually in it and closes the file.
func (i *index) Close() error {
	if err := i.mmap.Sync(gommap.MS_SYNC); err != nil {
		return err
	}
	if err := i.file.Sync(); err != nil {
		return err
	}
	if err := i.file.Truncate(int64(i.size)); err != nil {
		return err
	}
	return nil
}

// Read(int64) takes in an offset and returns the associated record’s position in the store.
// The given offset is relative to the segment’s base offset; 0 is always the offset of the index’s first entry, 1 is the second entry, and so on.
// We use relative offsets to reduce the size of the indexes by storing offsets as uint32s.
// If we used absolute offsets, we’d have to store the offsets as uint64s and require four more bytes for each entry.
// Four bytes doesn’t sound like much, until you multiply it by the number of records people often use distributed logs for, which with a company like LinkedIn is trillions of records every day.
// Even relatively small companies can make billions of records per day.
func (i *index) Read(in int64) (out uint32, pos uint64, err error) {
	if i.size == 0 {
		return 0, 0, io.EOF
	}
	if in == -1 {
		out = uint32((i.size / entWidth) - 1)
	} else {
		out = uint32(in)
	}
	pos = uint64(out) * entWidth
	if i.size < pos+entWidth {
		return 0, 0, io.EOF
	}
	out = enc.Uint32(i.mmap[pos : pos+offWidth])
	pos = enc.Uint64(i.mmap[pos+offWidth : pos+entWidth])
	return out, pos, nil
}

// Write(off uint32, pos uint32) appends the given offset and position to the index.
// First, we validate that we have space to write the entry.
// If there’s space, we then encode the offset and position and write them to the memory-mapped file. Then we increment the position where the next write will go.
func (i *index) Write(off uint32, pos uint64) error {
	if uint64(len(i.mmap)) < i.size+entWidth {
		return io.EOF
	}
	enc.PutUint32(i.mmap[i.size:i.size+offWidth], off)
	enc.PutUint64(i.mmap[i.size+offWidth:i.size+entWidth], pos)
	i.size += uint64(entWidth)
	return nil
}

func (i *index) Name() string {
	return i.file.Name()
}
