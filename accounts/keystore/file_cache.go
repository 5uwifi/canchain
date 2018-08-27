package keystore

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mapset "github.com/deckarep/golang-set"
	"github.com/5uwifi/canchain/lib/log4j"
)

type fileCache struct {
	all     mapset.Set
	lastMod time.Time
	mu      sync.RWMutex
}

func (fc *fileCache) scan(keyDir string) (mapset.Set, mapset.Set, mapset.Set, error) {
	t0 := time.Now()

	files, err := ioutil.ReadDir(keyDir)
	if err != nil {
		return nil, nil, nil, err
	}
	t1 := time.Now()

	fc.mu.Lock()
	defer fc.mu.Unlock()

	all := mapset.NewThreadUnsafeSet()
	mods := mapset.NewThreadUnsafeSet()

	var newLastMod time.Time
	for _, fi := range files {
		path := filepath.Join(keyDir, fi.Name())
		if nonKeyFile(fi) {
			log4j.Trace("Ignoring file on account scan", "path", path)
			continue
		}
		all.Add(path)

		modified := fi.ModTime()
		if modified.After(fc.lastMod) {
			mods.Add(path)
		}
		if modified.After(newLastMod) {
			newLastMod = modified
		}
	}
	t2 := time.Now()

	deletes := fc.all.Difference(all)
	creates := all.Difference(fc.all)
	updates := mods.Difference(creates)

	fc.all, fc.lastMod = all, newLastMod
	t3 := time.Now()

	log4j.Debug("FS scan times", "list", t1.Sub(t0), "set", t2.Sub(t1), "diff", t3.Sub(t2))
	return creates, deletes, updates, nil
}

func nonKeyFile(fi os.FileInfo) bool {
	if strings.HasSuffix(fi.Name(), "~") || strings.HasPrefix(fi.Name(), ".") {
		return true
	}
	if fi.IsDir() || fi.Mode()&os.ModeType != 0 {
		return true
	}
	return false
}
