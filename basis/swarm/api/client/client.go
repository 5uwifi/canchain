
package client

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/5uwifi/canchain/basis/swarm/api"
)

var (
	DefaultGateway = "http://localhost:8500"
	DefaultClient  = NewClient(DefaultGateway)
)

func NewClient(gateway string) *Client {
	return &Client{
		Gateway: gateway,
	}
}

type Client struct {
	Gateway string
}

func (c *Client) UploadRaw(r io.Reader, size int64, toEncrypt bool) (string, error) {
	if size <= 0 {
		return "", errors.New("data size must be greater than zero")
	}
	addr := ""
	if toEncrypt {
		addr = "encrypt"
	}
	req, err := http.NewRequest("POST", c.Gateway+"/bzz-raw:/"+addr, r)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status: %s", res.Status)
	}
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (c *Client) DownloadRaw(hash string) (io.ReadCloser, bool, error) {
	uri := c.Gateway + "/bzz-raw:/" + hash
	res, err := http.DefaultClient.Get(uri)
	if err != nil {
		return nil, false, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, false, fmt.Errorf("unexpected HTTP status: %s", res.Status)
	}
	isEncrypted := (res.Header.Get("X-Decrypted") == "true")
	return res.Body, isEncrypted, nil
}

type File struct {
	io.ReadCloser
	api.ManifestEntry
}

func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &File{
		ReadCloser: f,
		ManifestEntry: api.ManifestEntry{
			ContentType: mime.TypeByExtension(filepath.Ext(path)),
			Mode:        int64(stat.Mode()),
			Size:        stat.Size(),
			ModTime:     stat.ModTime(),
		},
	}, nil
}

// (if the manifest argument is non-empty) or creates a new manifest containing
func (c *Client) Upload(file *File, manifest string, toEncrypt bool) (string, error) {
	if file.Size <= 0 {
		return "", errors.New("file size must be greater than zero")
	}
	return c.TarUpload(manifest, &FileUploader{file}, toEncrypt)
}

func (c *Client) Download(hash, path string) (*File, error) {
	uri := c.Gateway + "/bzz:/" + hash + "/" + path
	res, err := http.DefaultClient.Get(uri)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("unexpected HTTP status: %s", res.Status)
	}
	return &File{
		ReadCloser: res.Body,
		ManifestEntry: api.ManifestEntry{
			ContentType: res.Header.Get("Content-Type"),
			Size:        res.ContentLength,
		},
	}, nil
}

// (i.e. bzz:/<hash>/)
func (c *Client) UploadDirectory(dir, defaultPath, manifest string, toEncrypt bool) (string, error) {
	stat, err := os.Stat(dir)
	if err != nil {
		return "", err
	} else if !stat.IsDir() {
		return "", fmt.Errorf("not a directory: %s", dir)
	}
	return c.TarUpload(manifest, &DirectoryUploader{dir, defaultPath}, toEncrypt)
}

func (c *Client) DownloadDirectory(hash, path, destDir string) error {
	stat, err := os.Stat(destDir)
	if err != nil {
		return err
	} else if !stat.IsDir() {
		return fmt.Errorf("not a directory: %s", destDir)
	}

	uri := c.Gateway + "/bzz:/" + hash + "/" + path
	req, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/x-tar")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status: %s", res.Status)
	}
	tr := tar.NewReader(res.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		// ignore the default path file
		if hdr.Name == "" {
			continue
		}

		dstPath := filepath.Join(destDir, filepath.Clean(strings.TrimPrefix(hdr.Name, path)))
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return err
		}
		var mode os.FileMode = 0644
		if hdr.Mode > 0 {
			mode = os.FileMode(hdr.Mode)
		}
		dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		n, err := io.Copy(dst, tr)
		dst.Close()
		if err != nil {
			return err
		} else if n != hdr.Size {
			return fmt.Errorf("expected %s to be %d bytes but got %d", hdr.Name, hdr.Size, n)
		}
	}
}

func (c *Client) DownloadFile(hash, path, dest string) error {
	hasDestinationFilename := false
	if stat, err := os.Stat(dest); err == nil {
		hasDestinationFilename = !stat.IsDir()
	} else {
		if os.IsNotExist(err) {
			// does not exist - should be created
			hasDestinationFilename = true
		} else {
			return fmt.Errorf("could not stat path: %v", err)
		}
	}

	manifestList, err := c.List(hash, path)
	if err != nil {
		return fmt.Errorf("could not list manifest: %v", err)
	}

	switch len(manifestList.Entries) {
	case 0:
		return fmt.Errorf("could not find path requested at manifest address. make sure the path you've specified is correct")
	case 1:
		//continue
	default:
		return fmt.Errorf("got too many matches for this path")
	}

	uri := c.Gateway + "/bzz:/" + hash + "/" + path
	req, err := http.NewRequest("GET", uri, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status: expected 200 OK, got %d", res.StatusCode)
	}
	filename := ""
	if hasDestinationFilename {
		filename = dest
	} else {
		// try to assert
		re := regexp.MustCompile("[^/]+$") //everything after last slash

		if results := re.FindAllString(path, -1); len(results) > 0 {
			filename = results[len(results)-1]
		} else {
			if entry := manifestList.Entries[0]; entry.Path != "" && entry.Path != "/" {
				filename = entry.Path
			} else {
				// assume hash as name if there's nothing from the command line
				filename = hash
			}
		}
		filename = filepath.Join(dest, filename)
	}
	filePath, err := filepath.Abs(filename)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0777); err != nil {
		return err
	}

	dst, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, res.Body)
	return err
}

func (c *Client) UploadManifest(m *api.Manifest, toEncrypt bool) (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return c.UploadRaw(bytes.NewReader(data), int64(len(data)), toEncrypt)
}

func (c *Client) DownloadManifest(hash string) (*api.Manifest, bool, error) {
	res, isEncrypted, err := c.DownloadRaw(hash)
	if err != nil {
		return nil, isEncrypted, err
	}
	defer res.Close()
	var manifest api.Manifest
	if err := json.NewDecoder(res).Decode(&manifest); err != nil {
		return nil, isEncrypted, err
	}
	return &manifest, isEncrypted, nil
}

//
//
//
//
// - a prefix of ""      would return [dir1/, file1.txt, file2.txt]
// - a prefix of "file"  would return [file1.txt, file2.txt]
// - a prefix of "dir1/" would return [dir1/dir2/, dir1/file3.txt]
//
func (c *Client) List(hash, prefix string) (*api.ManifestList, error) {
	res, err := http.DefaultClient.Get(c.Gateway + "/bzz-list:/" + hash + "/" + prefix)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status: %s", res.Status)
	}
	var list api.ManifestList
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		return nil, err
	}
	return &list, nil
}

type Uploader interface {
	Upload(UploadFn) error
}

type UploaderFunc func(UploadFn) error

func (u UploaderFunc) Upload(upload UploadFn) error {
	return u(upload)
}

type DirectoryUploader struct {
	Dir         string
	DefaultPath string
}

func (d *DirectoryUploader) Upload(upload UploadFn) error {
	if d.DefaultPath != "" {
		file, err := Open(d.DefaultPath)
		if err != nil {
			return err
		}
		if err := upload(file); err != nil {
			return err
		}
	}
	return filepath.Walk(d.Dir, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if f.IsDir() {
			return nil
		}
		file, err := Open(path)
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(d.Dir, path)
		if err != nil {
			return err
		}
		file.Path = filepath.ToSlash(relPath)
		return upload(file)
	})
}

type FileUploader struct {
	File *File
}

func (f *FileUploader) Upload(upload UploadFn) error {
	return upload(f.File)
}

type UploadFn func(file *File) error

func (c *Client) TarUpload(hash string, uploader Uploader, toEncrypt bool) (string, error) {
	reqR, reqW := io.Pipe()
	defer reqR.Close()
	addr := hash

	// If there is a hash already (a manifest), then that manifest will determine if the upload has
	// to be encrypted or not. If there is no manifest then the toEncrypt parameter decides if
	// there is encryption or not.
	if hash == "" && toEncrypt {
		// This is the built-in address for the encrypted upload endpoint
		addr = "encrypt"
	}
	req, err := http.NewRequest("POST", c.Gateway+"/bzz:/"+addr, reqR)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-tar")

	// use 'Expect: 100-continue' so we don't send the request body if
	// the server refuses the request
	req.Header.Set("Expect", "100-continue")

	tw := tar.NewWriter(reqW)

	// define an UploadFn which adds files to the tar stream
	uploadFn := func(file *File) error {
		hdr := &tar.Header{
			Name:    file.Path,
			Mode:    file.Mode,
			Size:    file.Size,
			ModTime: file.ModTime,
			Xattrs: map[string]string{
				"user.swarm.content-type": file.ContentType,
			},
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = io.Copy(tw, file)
		return err
	}

	// run the upload in a goroutine so we can send the request headers and
	// wait for a '100 Continue' response before sending the tar stream
	go func() {
		err := uploader.Upload(uploadFn)
		if err == nil {
			err = tw.Close()
		}
		reqW.CloseWithError(err)
	}()

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status: %s", res.Status)
	}
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (c *Client) MultipartUpload(hash string, uploader Uploader) (string, error) {
	reqR, reqW := io.Pipe()
	defer reqR.Close()
	req, err := http.NewRequest("POST", c.Gateway+"/bzz:/"+hash, reqR)
	if err != nil {
		return "", err
	}

	// use 'Expect: 100-continue' so we don't send the request body if
	// the server refuses the request
	req.Header.Set("Expect", "100-continue")

	mw := multipart.NewWriter(reqW)
	req.Header.Set("Content-Type", fmt.Sprintf("multipart/form-data; boundary=%q", mw.Boundary()))

	// define an UploadFn which adds files to the multipart form
	uploadFn := func(file *File) error {
		hdr := make(textproto.MIMEHeader)
		hdr.Set("Content-Disposition", fmt.Sprintf("form-data; name=%q", file.Path))
		hdr.Set("Content-Type", file.ContentType)
		hdr.Set("Content-Length", strconv.FormatInt(file.Size, 10))
		w, err := mw.CreatePart(hdr)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, file)
		return err
	}

	// run the upload in a goroutine so we can send the request headers and
	// wait for a '100 Continue' response before sending the multipart form
	go func() {
		err := uploader.Upload(uploadFn)
		if err == nil {
			err = mw.Close()
		}
		reqW.CloseWithError(err)
	}()

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status: %s", res.Status)
	}
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
