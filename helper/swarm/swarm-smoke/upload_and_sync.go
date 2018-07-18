
package main

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/5uwifi/canchain/basis/log4j"
	"github.com/pborman/uuid"

	cli "gopkg.in/urfave/cli.v1"
)

func generateEndpoints(scheme string, cluster string, from int, to int) {
	for port := from; port <= to; port++ {
		endpoints = append(endpoints, fmt.Sprintf("%s://%v.%s.swarm-gateways.net", scheme, port, cluster))
	}

	if includeLocalhost {
		endpoints = append(endpoints, "http://localhost:8500")
	}
}

func cliUploadAndSync(c *cli.Context) error {
	defer func(now time.Time) { log4j.Info("total time", "time", time.Since(now), "size", filesize) }(time.Now())

	generateEndpoints(scheme, cluster, from, to)

	log4j.Info("uploading to " + endpoints[0] + " and syncing")

	f, cleanup := generateRandomFile(filesize * 1000000)
	defer cleanup()

	hash, err := upload(f, endpoints[0])
	if err != nil {
		log4j.Error(err.Error())
		return err
	}

	fhash, err := digest(f)
	if err != nil {
		log4j.Error(err.Error())
		return err
	}

	log4j.Info("uploaded successfully", "hash", hash, "digest", fmt.Sprintf("%x", fhash))

	if filesize < 10 {
		time.Sleep(15 * time.Second)
	} else {
		time.Sleep(2 * time.Duration(filesize) * time.Second)
	}

	wg := sync.WaitGroup{}
	for _, endpoint := range endpoints {
		endpoint := endpoint
		ruid := uuid.New()[:8]
		wg.Add(1)
		go func(endpoint string, ruid string) {
			for {
				err := fetch(hash, endpoint, fhash, ruid)
				if err != nil {
					continue
				}

				wg.Done()
				return
			}
		}(endpoint, ruid)
	}
	wg.Wait()
	log4j.Info("all endpoints synced random file successfully")

	return nil
}

func fetch(hash string, endpoint string, original []byte, ruid string) error {
	log4j.Trace("sleeping", "ruid", ruid)
	time.Sleep(1 * time.Second)

	log4j.Trace("http get request", "ruid", ruid, "api", endpoint, "hash", hash)
	res, err := http.Get(endpoint + "/bzz:/" + hash + "/")
	if err != nil {
		log4j.Warn(err.Error(), "ruid", ruid)
		return err
	}
	log4j.Trace("http get response", "ruid", ruid, "api", endpoint, "hash", hash, "code", res.StatusCode, "len", res.ContentLength)

	if res.StatusCode != 200 {
		err := fmt.Errorf("expected status code %d, got %v", 200, res.StatusCode)
		log4j.Warn(err.Error(), "ruid", ruid)
		return err
	}

	defer res.Body.Close()

	rdigest, err := digest(res.Body)
	if err != nil {
		log4j.Warn(err.Error(), "ruid", ruid)
		return err
	}

	if !bytes.Equal(rdigest, original) {
		err := fmt.Errorf("downloaded imported file md5=%x is not the same as the generated one=%x", rdigest, original)
		log4j.Warn(err.Error(), "ruid", ruid)
		return err
	}

	log4j.Trace("downloaded file matches random file", "ruid", ruid, "len", res.ContentLength)

	return nil
}

func upload(f *os.File, endpoint string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command("swarm", "--bzzapi", endpoint, "up", f.Name())
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	hash := strings.TrimRight(out.String(), "\r\n")
	return hash, nil
}

func digest(r io.Reader) ([]byte, error) {
	h := md5.New()
	_, err := io.Copy(h, r)
	if err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func generateRandomFile(size int) (f *os.File, teardown func()) {
	// create a tmp file
	tmp, err := ioutil.TempFile("", "swarm-test")
	if err != nil {
		panic(err)
	}

	// callback for tmp file cleanup
	teardown = func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}

	buf := make([]byte, size)
	_, err = rand.Read(buf)
	if err != nil {
		panic(err)
	}
	ioutil.WriteFile(tmp.Name(), buf, 0755)

	return tmp, teardown
}
