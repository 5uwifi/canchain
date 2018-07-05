//
// (at your option) any later version.
//
//

package keystore

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/5uwifi/canchain/basis/log4j"
	set "gopkg.in/fatih/set.v0"
)

type fileCache struct {
	all     *set.SetNonTS // Set of all files from the keystore folder
	lastMod time.Time     // Last time instance when a file was modified
	mu      sync.RWMutex
}

func (fc *fileCache) scan(keyDir string) (set.Interface, set.Interface, set.Interface, error) {
	t0 := time.Now()

	// List all the failes from the keystore folder
	files, err := ioutil.ReadDir(keyDir)
	if err != nil {
		return nil, nil, nil, err
	}
	t1 := time.Now()

	fc.mu.Lock()
	defer fc.mu.Unlock()

	// Iterate all the files and gather their metadata
	all := set.NewNonTS()
	mods := set.NewNonTS()

	var newLastMod time.Time
	for _, fi := range files {
		// Skip any non-key files from the folder
		path := filepath.Join(keyDir, fi.Name())
		if skipKeyFile(fi) {
			log4j.Trace("Ignoring file on account scan", "path", path)
			continue
		}
		// Gather the set of all and fresly modified files
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

	// Update the tracked files and return the three sets
	deletes := set.Difference(fc.all, all)   // Deletes = previous - current
	creates := set.Difference(all, fc.all)   // Creates = current - previous
	updates := set.Difference(mods, creates) // Updates = modified - creates

	fc.all, fc.lastMod = all, newLastMod
	t3 := time.Now()

	// Report on the scanning stats and return
	log4j.Debug("FS scan times", "list", t1.Sub(t0), "set", t2.Sub(t1), "diff", t3.Sub(t2))
	return creates, deletes, updates, nil
}

func skipKeyFile(fi os.FileInfo) bool {
	// Skip editor backups and UNIX-style hidden files.
	if strings.HasSuffix(fi.Name(), "~") || strings.HasPrefix(fi.Name(), ".") {
		return true
	}
	// Skip misc special files, directories (yes, symlinks too).
	if fi.IsDir() || fi.Mode()&os.ModeType != 0 {
		return true
	}
	return false
}
