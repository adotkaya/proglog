package log

import (
	"bufio"
	"encoding/binary"
	"os"
	"sync"
)

var (
	enc = binary.BigEndian
)

const (
	lenWidth = 8
)

type Store struct {
	file *os.File
	buf  *bufio.Writer
	size uint64
	mu   sync.Mutex
}

func NewStore(f *os.File) (*Store, error) {
	fi, err := os.Stat(f.Name())
	if err != nil {
		return nil, err
	}
	size := uint64(fi.Size())
	return &Store{
		file: f,
		size: size,
		buf:  bufio.NewWriter(f),
	}, nil
}

func (s *Store) Append(p []byte) (n uint64, pos uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pos = s.size
	if err := binary.Write(s.buf, enc, uint64(len(p))); err != nil {
		return 0, 0, err
	}
	w, err := s.buf.Write(p)
	if err != nil {
		return 0, 0, err
	}
	w += lenWidth
	s.size += uint64(w)
	return uint64(w), pos, nil
}

func (s *Store) Read(pos uint64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.buf.Flush(); err != nil {
		return nil, err
	}
	size := make([]byte, lenWidth)
	if _, err := s.file.ReadAt(size, int64(pos)); err != nil {
		return nil, err
	}
	b := make([]byte, enc.Uint64(size))
	if _, err := s.file.ReadAt(b, int64(pos)+lenWidth); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Store) ReadAt(p []byte, off int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.buf.Flush(); err != nil {
		return 0, err
	}
	return s.file.ReadAt(p, off)
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.buf.Flush()
	if err != nil {
		return err
	}
	return s.file.Close()
}
