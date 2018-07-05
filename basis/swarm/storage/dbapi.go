//
// (at your option) any later version.
//
//

package storage

type DBAPI struct {
	db  *LDBStore
	loc *LocalStore
}

func NewDBAPI(loc *LocalStore) *DBAPI {
	return &DBAPI{loc.DbStore, loc}
}

func (d *DBAPI) Get(addr Address) (*Chunk, error) {
	return d.loc.Get(addr)
}

func (d *DBAPI) CurrentBucketStorageIndex(po uint8) uint64 {
	return d.db.CurrentBucketStorageIndex(po)
}

func (d *DBAPI) Iterator(from uint64, to uint64, po uint8, f func(Address, uint64) bool) error {
	return d.db.SyncIterator(from, to, po, f)
}

func (d *DBAPI) GetOrCreateRequest(addr Address) (*Chunk, bool) {
	return d.loc.GetOrCreateRequest(addr)
}

func (d *DBAPI) Put(chunk *Chunk) {
	d.loc.Put(chunk)
}
