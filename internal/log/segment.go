// START: intro
package log

import (
	"fmt"
	"os"
	"path"

	api "github.com/adotkaya/proglog/api/v1"
	"google.golang.org/protobuf/proto"
)

type segment struct {
	store                  *Store
	index                  *index
	baseOffset, nextOffset uint64
	config                 Config
}

func newSegment(dir string, baseOffset uint64, c Config) (*segment, error) {
	s := &segment{
		baseOffset: baseOffset,
		config:     c,
	}
	var err error
	storeFile, err := os.OpenFile(
		path.Join(dir, fmt.Sprintf("%d%s", baseOffset, ".store")),
		os.O_RDWR|os.O_CREATE|os.O_APPEND,
		0644,
	)
	if err != nil {
		return nil, err
	}
	if s.store, err = NewStore(storeFile); err != nil {
		return nil, err
	}
	indexFile, err := os.OpenFile(
		path.Join(dir, fmt.Sprintf("%d%s", baseOffset, ".index")),
		os.O_RDWR|os.O_CREATE,
		0644,
	)
	if err != nil {
		return nil, err
	}
	if s.index, err = newIndex(indexFile, c); err != nil {
		return nil, err
	}
	// If the store is empty, the segment is empty regardless of index state
	// (the index file may be pre-allocated with zeros by os.Truncate).
	if s.store.size == 0 {
		s.nextOffset = baseOffset
		s.index.size = 0
	} else {
		// Recompute the index's logical size from actual entries,
		// because the index file may have been pre-allocated and
		// never truncated back down (e.g., container killed before
		// index.Close() could truncate).
		var logicalSize uint64
		for rel := uint64(0); ; rel++ {
			off, _, err := s.index.Read(int64(rel))
			if err != nil || uint64(off) != rel {
				break
			}
			logicalSize += entWidth
		}
		s.index.size = logicalSize
		if off, _, err := s.index.Read(-1); err != nil {
			s.nextOffset = baseOffset
		} else {
			s.nextOffset = baseOffset + uint64(off) + 1
		}
	}
	return s, nil
}

func (s *segment) Append(record *api.Record) (offset uint64, err error) {
	cur := s.nextOffset
	record.Offset = uint64(cur)
	p, err := proto.Marshal(record)
	if err != nil {
		return 0, err
	}
	_, pos, err := s.store.Append(p)
	if err != nil {
		return 0, err
	}
	if err = s.index.Write(
		// index offsets are relative to base offset
		uint32(s.nextOffset-uint64(s.baseOffset)),
		pos,
	); err != nil {
		return 0, err
	}
	s.nextOffset++
	return cur, nil
}

func (s *segment) Read(off uint64) (*api.Record, error) {
	_, pos, err := s.index.Read(int64(off - s.baseOffset))
	if err != nil {
		return nil, err
	}
	p, err := s.store.Read(pos)
	if err != nil {
		return nil, err
	}
	record := &api.Record{}
	err = proto.Unmarshal(p, record)
	return record, err
}

func (s *segment) IsMaxed() bool {
	return s.store.size >= s.config.Segment.MaxStoreBytes ||
		s.index.size >= s.config.Segment.MaxIndexBytes
}

func (s *segment) Close() error {
	if err := s.index.Close(); err != nil {
		return err
	}
	if err := s.store.Close(); err != nil {
		return err
	}
	return nil
}

func (s *segment) Remove() error {
	if err := s.Close(); err != nil {
		return err
	}
	if err := os.Remove(s.index.Name()); err != nil {
		return err
	}
	if err := os.Remove(s.store.file.Name()); err != nil {
		return err
	}
	return nil
}

func nearestMultiple(j, k uint64) uint64 {
	if k == 0 {
		return 0
	}
	return (j / k) * k

}
