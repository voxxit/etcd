package storage

import (
	"log"
	"sync"

	"github.com/coreos/etcd/Godeps/_workspace/src/github.com/google/btree"
)

type index interface {
	Get(key []byte, atRev int64) (rev reversion, err error)
	Range(key, end []byte, atRev int64) ([][]byte, []reversion)
	Put(key []byte, rev reversion)
	Tombstone(key []byte, rev reversion) error
	Compact(rev int64) map[reversion]struct{}
}

type treeIndex struct {
	sync.RWMutex
	tree *btree.BTree
}

func newTreeIndex() index {
	return &treeIndex{
		tree: btree.New(32),
	}
}

func (ti *treeIndex) Put(key []byte, rev reversion) {
	keyi := &keyIndex{key: key}

	ti.Lock()
	defer ti.Unlock()
	item := ti.tree.Get(keyi)
	if item == nil {
		keyi.put(rev.main, rev.sub)
		ti.tree.ReplaceOrInsert(keyi)
		return
	}
	okeyi := item.(*keyIndex)
	okeyi.put(rev.main, rev.sub)
}

func (ti *treeIndex) Get(key []byte, atRev int64) (rev reversion, err error) {
	keyi := &keyIndex{key: key}

	ti.RLock()
	defer ti.RUnlock()
	item := ti.tree.Get(keyi)
	if item == nil {
		return reversion{}, ErrReversionNotFound
	}

	keyi = item.(*keyIndex)
	return keyi.get(atRev)
}

func (ti *treeIndex) Range(key, end []byte, atRev int64) (keys [][]byte, revs []reversion) {
	if end == nil {
		rev, err := ti.Get(key, atRev)
		if err != nil {
			return nil, nil
		}
		return [][]byte{key}, []reversion{rev}
	}

	keyi := &keyIndex{key: key}
	endi := &keyIndex{key: end}

	ti.RLock()
	defer ti.RUnlock()

	ti.tree.AscendGreaterOrEqual(keyi, func(item btree.Item) bool {
		if !item.Less(endi) {
			return false
		}
		curKeyi := item.(*keyIndex)
		rev, err := curKeyi.get(atRev)
		if err != nil {
			return true
		}
		revs = append(revs, rev)
		keys = append(keys, curKeyi.key)
		return true
	})

	return keys, revs
}

func (ti *treeIndex) Tombstone(key []byte, rev reversion) error {
	keyi := &keyIndex{key: key}

	ti.Lock()
	defer ti.Unlock()
	item := ti.tree.Get(keyi)
	if item == nil {
		return ErrReversionNotFound
	}

	ki := item.(*keyIndex)
	ki.tombstone(rev.main, rev.sub)
	return nil
}

func (ti *treeIndex) Compact(rev int64) map[reversion]struct{} {
	available := make(map[reversion]struct{})
	emptyki := make([]*keyIndex, 0)
	log.Printf("store.index: compact %d", rev)
	// TODO: do not hold the lock for long time?
	// This is probably OK. Compacting 10M keys takes O(10ms).
	ti.Lock()
	defer ti.Unlock()
	ti.tree.Ascend(compactIndex(rev, available, &emptyki))
	for _, ki := range emptyki {
		item := ti.tree.Delete(ki)
		if item == nil {
			log.Panic("store.index: unexpected delete failure during compaction")
		}
	}
	return available
}

func compactIndex(rev int64, available map[reversion]struct{}, emptyki *[]*keyIndex) func(i btree.Item) bool {
	return func(i btree.Item) bool {
		keyi := i.(*keyIndex)
		keyi.compact(rev, available)
		if keyi.isEmpty() {
			*emptyki = append(*emptyki, keyi)
		}
		return true
	}
}
