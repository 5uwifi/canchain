
package api

import (
	"path"

	"github.com/5uwifi/canchain/basis/swarm/storage"
)

type Response struct {
	MimeType string
	Status   int
	Size     int64
	// Content  []byte
	Content string
}

//
type Storage struct {
	api *API
}

func NewStorage(api *API) *Storage {
	return &Storage{api}
}

//
func (s *Storage) Put(content, contentType string, toEncrypt bool) (storage.Address, func(), error) {
	return s.api.Put(content, contentType, toEncrypt)
}

//
func (s *Storage) Get(bzzpath string) (*Response, error) {
	uri, err := Parse(path.Join("bzz:/", bzzpath))
	if err != nil {
		return nil, err
	}
	addr, err := s.api.Resolve(uri)
	if err != nil {
		return nil, err
	}
	reader, mimeType, status, _, err := s.api.Get(addr, uri.Path)
	if err != nil {
		return nil, err
	}
	quitC := make(chan bool)
	expsize, err := reader.Size(quitC)
	if err != nil {
		return nil, err
	}
	body := make([]byte, expsize)
	size, err := reader.Read(body)
	if int64(size) == expsize {
		err = nil
	}
	return &Response{mimeType, status, expsize, string(body[:size])}, err
}

//
func (s *Storage) Modify(rootHash, path, contentHash, contentType string) (newRootHash string, err error) {
	uri, err := Parse("bzz:/" + rootHash)
	if err != nil {
		return "", err
	}
	addr, err := s.api.Resolve(uri)
	if err != nil {
		return "", err
	}
	addr, err = s.api.Modify(addr, path, contentHash, contentType)
	if err != nil {
		return "", err
	}
	return addr.Hex(), nil
}
