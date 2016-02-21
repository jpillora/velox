// Code generated by go-bindata.
// sources:
// dist/velox.min.js
// DO NOT EDIT!

package assets

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func bindataRead(data []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("Read %q: %v", name, err)
	}

	var buf bytes.Buffer
	_, err = io.Copy(&buf, gz)
	clErr := gz.Close()

	if err != nil {
		return nil, fmt.Errorf("Read %q: %v", name, err)
	}
	if clErr != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

type asset struct {
	bytes []byte
	info  os.FileInfo
}

type bindataFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
}

func (fi bindataFileInfo) Name() string {
	return fi.name
}
func (fi bindataFileInfo) Size() int64 {
	return fi.size
}
func (fi bindataFileInfo) Mode() os.FileMode {
	return fi.mode
}
func (fi bindataFileInfo) ModTime() time.Time {
	return fi.modTime
}
func (fi bindataFileInfo) IsDir() bool {
	return false
}
func (fi bindataFileInfo) Sys() interface{} {
	return nil
}

var _distVeloxMinJs = []byte("\x1f\x8b\x08\x00\x00\x09\x6e\x88\x00\xff\x8c\x58\x6f\x6f\xdb\x36\x13\x7f\xdf\x4f\x21\x13\x85\x21\xce\xb4\xac\x64\xdd\xf3\xc2\x2a\x9b\xa7\xc8\x82\x35\x43\x96\x0e\x4b\x1e\x74\x40\x9a\x15\x8a\xc4\xd8\xea\x14\x52\x8f\x48\x39\x09\x62\x7f\xf7\xdd\x91\x94\x25\xc5\x4e\xd7\x37\xb1\x48\xde\x1d\xc9\xfb\xdd\x9f\x1f\x33\x9b\x05\x2b\x51\xaa\x87\x60\x1a\xac\xe2\xe8\x30\x8a\xe1\x63\x69\x4c\xa5\xe7\xb3\xd9\xa2\x30\xcb\xe6\x26\xca\xd4\xdd\xec\x6b\x55\x94\xa5\xaa\xd3\x99\x15\x7e\x05\x5a\xbf\xa6\xc5\x9d\x08\x7e\x77\xd3\xc1\xdb\x5c\xac\xfe\xdb\x0a\xa1\xc6\x3b\xb0\xf3\xdb\xe9\x65\x70\xac\xaa\xc7\xba\x58\x2c\x4d\x70\x18\x1f\xfc\xe7\xd5\xe8\xb6\x91\x99\x29\x94\x0c\x0d\x13\xf4\xa9\x1d\x05\x30\xa6\x4f\x75\xa4\x64\x59\x48\xc1\x65\xba\x2a\x16\xa9\x51\x38\x71\x06\x13\xc9\xad\xaa\xc3\x55\x5a\x07\x82\xc7\x89\x78\xab\xa3\x52\xc8\x85\x59\x26\x62\x32\xa1\xad\xd2\x78\xac\xaf\xc4\x75\x94\x36\xa0\x26\x4c\xfd\xe8\xc7\xf6\x3b\xa4\x9b\xed\x4e\x05\xee\xcc\x24\x7d\xd2\xf7\x85\xc9\x96\xb8\x6f\x96\x6a\x11\xd4\xd1\xa7\x8b\xb9\x59\x16\x3a\xba\xd7\x7c\x14\x27\x37\xb5\x48\xff\x4e\xfc\xd2\xc5\xc5\x89\x5b\xd3\x5a\x74\x8b\xb9\xb8\x4d\x9b\xd2\xc0\x4a\xad\xee\xc9\xe5\x63\x25\x82\xbb\x46\x9b\xe0\x46\x38\x9f\x82\xc5\x40\xd5\xfe\x1b\x4c\x90\x4d\x71\x1b\x8a\xf5\x3a\x14\x9c\x38\x47\x12\xca\x66\x7f\x85\xf7\x7a\x8d\x2e\xa7\xfa\x68\x3e\x8b\x8c\xd0\x26\x14\xd4\x4a\x2d\xa3\xaa\x56\x46\x65\xaa\x9c\x90\xd9\x8c\x4c\x96\xd1\x52\x69\x33\x11\x94\xf9\x83\x8e\xc7\x20\x25\xe0\x92\x55\x99\x66\x22\x9c\xfd\x85\x76\x66\x8c\xdc\x6b\x42\xbd\x50\x53\x97\x5c\xb0\x91\x5c\xaf\x89\xba\xf9\x2a\x32\x43\x46\xdc\xc0\x51\xd5\x6d\x20\xa9\x3b\xf9\xa9\x5c\xa5\x65\x91\x07\x7e\x3d\xb1\x7a\x30\xe0\xd2\x99\x58\x89\x5a\x83\xeb\x78\xec\x86\x4a\x36\x55\x9e\x1a\xc1\xb7\x58\xd2\xa7\x4d\xbb\x24\xea\x5a\xd5\x7b\x56\x32\x25\x25\x18\x17\x39\x1f\x1d\x0c\x66\x00\x1b\x44\x56\x31\xcd\x6a\x96\xb2\x8c\x2d\x13\xc0\xac\xa9\x65\x60\xa2\x4f\xe2\xe6\x42\x65\x7f\x0b\x73\x14\x2a\x4e\x56\x87\x84\x69\x7e\x75\xcd\x7a\xf6\x3d\x96\xd6\x02\x97\xe2\x7e\x0b\x6f\x6b\x44\x47\x55\xa3\x97\xa1\xa2\x4c\x6d\x18\x62\xcc\x9f\xf0\x17\x30\x1e\x46\xa2\x17\xaf\x43\x94\x61\x38\x85\x62\x00\x9a\x93\x47\xdc\x5f\x54\x00\xa9\xad\x86\x05\x8c\x2b\xb6\x8d\xe5\x51\xcc\xd2\x67\xaa\x10\x05\x23\xb3\x07\x0f\x98\x1b\x89\x3d\xf3\x82\xfa\xcd\x44\x82\x17\x95\x09\x18\x30\x41\x21\xb5\x49\x65\x86\x02\xef\xeb\x3a\x85\x80\x17\x3b\x73\x14\x13\x27\x31\x3e\x5f\xde\x89\x36\x71\xa8\x89\x2a\x55\x85\x34\x11\x25\xc4\x37\x0a\x41\x62\x80\xc7\x29\x79\x4d\x38\xe7\xf2\x2a\xbe\x5e\xaf\xed\x14\x9c\x27\x17\xa5\x30\x22\x30\x57\xf2\x3a\xd9\x8a\x0a\x8a\x63\x9e\x86\xf8\xc3\x04\xfc\xd9\xba\xdc\x00\xe4\x51\x9a\xe7\x27\x2b\x21\xcd\x59\xa1\x8d\x80\xa8\x08\x89\xf3\x07\x01\x6c\xf6\x2f\xdf\xde\x6e\xd7\x33\x7e\x45\xee\x84\xd6\xe9\x02\xc6\xc4\x86\x14\xfc\xaa\x4a\x48\xf8\xc9\x4a\xa5\x05\xb9\x66\x4b\x0e\xf7\x52\x59\x8a\x7e\x65\x85\x73\x3c\x3a\x8c\x3f\xf9\xc8\x9a\xf7\xa2\xd0\x46\xdc\xb6\x36\x20\x28\x76\xa6\xad\x0e\xcc\x7e\xf4\x15\xd0\xd1\x00\x56\x92\x95\x22\xad\x2f\xa1\xd4\xa9\xc6\x84\x9d\x4e\x64\x68\x17\xc5\xe3\xb1\xfb\x04\x51\x48\x8d\xd0\xaf\x80\xdb\xd2\x47\x48\xe3\x6e\xc0\x0f\xe2\x18\x16\x79\x9b\x97\x93\x70\xf6\xf9\xc8\x67\x7c\x3b\x47\x8f\xc8\x98\xcc\xc9\x11\xa1\x13\x52\x71\x32\x51\x13\x32\x5e\xc1\x6f\x3f\x11\xdb\xec\x3f\xda\x9e\xc0\x86\xfe\x36\x5b\xa0\xa6\xcd\x87\x4b\xd6\xd7\x17\xaa\xa9\xa1\x48\x18\xf6\x04\x95\x6f\x79\x5c\x8b\x1c\x26\x8b\xb4\xd4\xf3\x51\xbc\xa1\x4c\xd8\x73\xb1\x2c\x02\x8c\x4f\x52\xa8\x8c\x5d\xd0\xd2\x27\x61\x6d\x5d\x01\x88\x70\x94\x6b\x2e\xae\x08\x8e\xf1\x3b\xba\x29\x64\x0e\x05\x6b\xe3\xaf\x5d\x15\x72\x11\x19\xae\x85\x39\x95\x46\xd4\x50\x59\xc2\x6e\xde\xca\xe2\x90\xb2\x1f\xc5\x1b\xf0\x7b\x5e\xe8\xef\x40\xab\xad\x18\xad\x83\x37\xcc\x7f\xf6\x95\x30\x29\xda\x4b\x7b\xfc\xec\x8d\x92\x3d\x37\x82\x24\x34\xbd\x1b\x89\x6b\x2e\x9b\xb2\x84\x2b\x84\x3b\xa0\xc2\x67\x3f\xad\x7a\x9e\xec\x49\x40\x50\xa4\xf9\xe3\x85\x81\xaa\x38\xe2\xbc\x27\x13\x1d\x9f\x7d\xbc\x38\xf9\x79\xbd\xde\x6b\x6c\x8b\xd8\x8b\xa6\xb6\x12\xde\x10\xed\x4b\xda\x4c\x08\x7b\x81\x68\x6f\xc1\x76\x43\xd6\x61\x42\x37\x1b\xa6\x85\xcc\xe7\x7d\x60\xdb\x9c\xfd\x97\x6b\xff\xcb\x49\x79\xff\xa4\x1f\x7f\x3f\x39\xef\x42\x33\xc2\x2d\x31\x22\x57\x0a\x5a\x4c\xbc\x61\x78\x98\x1d\xb0\xad\x10\xc1\x25\x82\xe8\x82\x9e\xcf\xff\xc1\x61\x2d\x05\x60\x12\x12\x1f\xfa\x4f\x8a\x65\xd0\xa9\x80\xa3\x00\x72\x08\x95\x27\xc1\x7f\xbd\xf8\x78\x1e\x55\x69\x0d\x9e\x91\x74\x03\xe5\x01\x70\x2f\xb6\xf7\xb4\x87\xe8\xb7\x2a\x58\xc3\xae\x3c\x12\xd1\x8d\xca\x21\x5d\x47\x6d\xf3\xa3\x2f\x6a\x10\xf4\xb2\x6f\x96\xd0\x67\x13\x81\xd9\x6d\xd2\xa3\xaf\x5a\xc9\x0a\xf7\x8b\xd2\xaa\x2a\x1f\xc3\xd6\x12\x73\xb6\xe9\x3c\xdd\x99\x62\xa4\xbd\x1e\x94\xdd\xb6\x0b\x78\x99\xe8\xb5\x35\xe3\x1d\xde\x4d\x84\xdf\xd0\xf2\x9d\xb9\xd5\xf1\xc3\x36\x44\xda\x3e\x2e\x86\x85\x64\x5b\x9b\x36\xce\xf3\x58\x67\x77\x00\xea\xf5\xef\xd8\x89\xd9\xe8\xfb\x96\xdc\x41\xcf\xfc\x0f\xfc\x90\x0d\xb3\x7a\x3c\xee\x98\xdb\xa0\xb0\x62\xe9\x18\x44\xaf\x37\xd9\x2f\x1e\x9d\x61\xea\xc3\xc5\x42\x33\x08\x16\xb8\x0d\x62\x17\x9a\xc8\x52\x2d\x5e\x53\x40\xa0\x14\xb5\x09\xc9\x25\xa8\x07\x37\x40\x7d\xb4\xa8\x83\x5c\x09\x1d\x48\x65\x02\xdd\x54\x95\xaa\x4d\x17\xeb\x00\xee\x26\xbc\x87\x5d\xd5\x3d\xcb\x55\xd6\xdc\x41\x5a\x33\x17\xc6\x94\xed\x89\x4b\x56\x24\xa4\x97\xfb\x04\x3b\xaa\xe5\x71\xc0\xf1\x3e\xeb\xf5\xe7\x26\x8e\xdf\xc7\x74\xb2\xee\x0f\x5e\xcf\x16\x10\xd0\x7d\x63\x43\x4e\xac\xa3\x2f\x95\x2a\x4b\x74\x48\xdd\xf7\x4c\xcf\xef\x2a\xca\xd2\xb2\x0c\x35\x78\xc2\xf4\x88\xae\x0a\x5d\x4a\x40\x74\xeb\x41\xaa\xea\xb6\x96\xb8\x08\x4f\x5c\xa9\xc4\x36\xf1\xe7\x6f\x67\x1f\x80\x40\xfe\x21\xfe\xdf\x40\x4f\x02\xde\x80\xa1\x10\x92\x5f\x4e\x2e\x81\x79\x45\xff\xfb\xe3\x8c\x8d\xb0\x81\x41\xb6\x1a\x2f\xf3\x01\xec\x62\xf7\x7e\x9f\x65\xa2\x32\xd0\x9d\x8d\x78\x30\x33\x81\x4e\x98\x6a\x03\xbb\xde\x91\xfd\x0a\xc7\x50\x8f\xc5\xf4\x58\x49\x53\xab\x12\xf4\xa4\x9a\x66\x38\xf5\x82\xf8\x9f\x53\x3f\x21\xf2\xe9\x27\xe8\x5e\xa0\x31\x3c\x2c\xe8\x61\x5a\x8e\x78\x0a\xc1\xbf\xc7\xc0\x59\xaa\xcd\xd4\x82\x33\x3d\xfd\x99\xb0\x14\x49\x06\x21\xb0\x97\x71\x0e\xe5\x3f\x89\x37\x30\x52\xd2\x7a\x4a\xa3\xa7\xb2\x65\x2a\x17\x03\x96\xeb\x48\x81\x62\x4b\x56\xb2\x86\xe5\xac\xc2\x12\xf4\x23\xe7\x3e\x7c\x5b\x17\xaf\xd7\x6f\x76\xe6\xc6\xe3\xc3\x38\xf6\xb3\x68\xbd\xd1\x88\xed\x33\x58\x3e\x9e\x9f\x9f\x1c\x5f\x9e\x9e\xff\x02\x29\x31\x58\xd4\xb6\xa8\x02\x06\xd0\x2f\x6d\x89\xb1\x37\x09\x3d\x25\x7a\xc2\x1a\x30\x77\x83\x0d\xf2\x7e\xb8\x59\x82\xd0\x9b\xf6\x14\xba\x52\x52\x8b\x4b\xc0\x06\xe8\x25\xf1\x55\xf1\x16\x72\x04\xf9\x9c\x82\x7a\xaa\x9b\x1b\x80\x2b\xcc\x3c\x41\xa4\x91\xae\xca\x02\x36\xf8\x2c\xc1\xb1\x4b\xde\xb1\xb1\x12\x09\x78\x03\x4f\x81\x1c\xdd\x97\x71\x93\x34\x6f\x55\x4b\x2b\x1b\x78\x8f\xe5\x5c\x5d\x35\xd7\xdb\x27\x89\x60\x04\x2c\xc0\xcd\xf3\x08\x12\x49\x3c\x7c\x84\x9a\x6d\xc3\x83\xd0\xa3\x25\x4c\x6e\x9f\x2e\x76\x72\x7e\xf4\x59\xff\x30\x43\x95\xf9\x50\xc5\xd6\x06\x50\x09\x1d\x23\xb0\xf5\x1d\xf8\x45\xd8\x33\xe0\x28\xdc\xd6\x00\x65\x85\x3e\x4f\xcf\x9d\x02\x3e\xa7\x6a\xee\x3e\x9f\x9b\xc6\x56\x02\x96\x4b\xf7\x50\xe8\x19\xc4\x85\x9e\xbd\x67\x6a\x45\x3e\x07\xad\xb4\x7f\x05\x98\x7a\xe9\xfc\x45\x6e\x85\x31\x44\xe7\x04\xaa\x76\x3e\x1e\x97\xde\x6b\x80\x75\xe5\x9f\x2e\x65\xf4\x55\x15\xd2\x7b\xdd\xd2\x41\x0c\xd4\xe7\xa8\x2f\x59\x45\x1d\x0e\x3d\x60\x68\xb2\x2f\xe6\x64\x58\xd3\x8d\xe5\xf8\x7a\xc8\x2b\xda\x1a\x00\x9b\xef\xea\x1d\x3d\x0f\xbe\x2e\x30\x77\x43\xd0\xb3\x73\x1f\x83\x6e\x04\x44\x0a\x77\xb6\x4e\xd8\x7b\x26\x2c\x55\xae\xe7\xc3\xf5\xf6\x16\x35\x78\x22\xdc\x40\x31\x0e\xad\xa4\xcf\x51\x74\xc5\x97\x87\x65\xcd\x8d\x8f\x60\x85\x39\xf4\x1d\xe7\x61\x16\x4a\x15\x79\x67\x6d\x80\x09\x61\x2a\x6b\xc7\x78\x6b\xfe\x53\x8c\x8f\x34\x47\x9e\x30\x77\xda\x07\x1a\xa4\x84\xa3\x17\x6d\x93\x75\x0f\xe6\x00\xd1\xba\x78\x94\x26\x7d\x38\x71\x8c\xe0\x1c\xda\x87\x90\xaa\x59\x2c\x83\xb4\x5e\xd8\x46\x81\xcc\xc0\xde\x1d\xca\x26\x37\xec\x99\x1b\x9c\x5b\x7a\x7e\xb5\xe3\x5e\xa9\xb7\x87\x71\x93\x78\x63\x3b\x54\xe8\x0d\x39\x78\xe9\xec\xed\xc1\x3b\xdb\x58\xa8\x1d\x2f\x1c\x92\xf2\x6e\x43\xda\x6d\xd6\x79\xbe\x3b\xdf\x3c\x66\x58\x81\xe6\x07\xcc\x19\x9b\x1f\xb2\x81\xdf\xe7\xc3\x17\xae\x7d\xa6\xb2\xc2\x6e\x7f\x45\xbe\xc0\x3b\x61\x42\x3e\xa4\x32\x87\xf6\xab\xc9\x35\xfa\xb7\xb0\x4f\x53\xc9\xe3\x44\xbe\x2d\xda\xfa\x21\xa1\x7e\x14\xf0\x94\x74\x0d\xcd\x82\x23\x9c\x17\xdb\x97\x87\x63\x36\xed\x68\x20\xb7\x61\xcf\x1f\x95\xcf\x0e\xb5\xff\x30\xfe\x85\xb6\xbb\x00\x09\xe6\x9c\xb2\xbb\xe4\x4a\x85\x7d\xf0\x8b\x3b\xb5\x12\xdf\xda\xf5\xfb\x5d\xd1\xba\x61\x7a\x90\xc8\x77\xe0\x98\xe9\x54\x52\x5c\xc6\x77\x36\xe7\xf8\x7f\x03\x5b\x95\xa1\xd2\x48\x76\x40\xdd\xff\xa0\x80\xe4\x78\x5a\x3a\x77\x21\xb2\xe5\xcd\x7e\x68\xc9\x9c\xfd\xee\xa2\x02\xc0\x84\xa0\x84\x3a\xb4\x81\x73\x3d\xff\x57\x8a\xa3\x56\x90\x2f\x6d\xd0\xaa\xba\x58\x14\x40\x1a\xdd\xa8\x84\x3e\x6a\xaf\x7b\x9a\x73\x69\x9b\xc9\xf0\xed\x6d\x33\xcd\x05\xaf\x4d\xc0\xae\x71\x74\x8a\xb0\x33\x73\x56\xed\x19\xc8\x9d\xca\x9b\xd2\xd1\x25\xa8\x47\x6e\x14\x89\x07\x24\x63\x9a\xdb\x7f\x17\xf4\x58\x15\x4c\x6c\x1c\x0b\x4c\x5e\xfd\x13\x00\x00\xff\xff\x1b\x8c\xf2\x57\xbd\x14\x00\x00")

func distVeloxMinJsBytes() ([]byte, error) {
	return bindataRead(
		_distVeloxMinJs,
		"dist/velox.min.js",
	)
}

func distVeloxMinJs() (*asset, error) {
	bytes, err := distVeloxMinJsBytes()
	if err != nil {
		return nil, err
	}

	info := bindataFileInfo{name: "dist/velox.min.js", size: 5309, mode: os.FileMode(420), modTime: time.Unix(1456098637, 0)}
	a := &asset{bytes: bytes, info: info}
	return a, nil
}

// Asset loads and returns the asset for the given name.
// It returns an error if the asset could not be found or
// could not be loaded.
func Asset(name string) ([]byte, error) {
	cannonicalName := strings.Replace(name, "\\", "/", -1)
	if f, ok := _bindata[cannonicalName]; ok {
		a, err := f()
		if err != nil {
			return nil, fmt.Errorf("Asset %s can't read by error: %v", name, err)
		}
		return a.bytes, nil
	}
	return nil, fmt.Errorf("Asset %s not found", name)
}

// MustAsset is like Asset but panics when Asset would return an error.
// It simplifies safe initialization of global variables.
func MustAsset(name string) []byte {
	a, err := Asset(name)
	if err != nil {
		panic("asset: Asset(" + name + "): " + err.Error())
	}

	return a
}

// AssetInfo loads and returns the asset info for the given name.
// It returns an error if the asset could not be found or
// could not be loaded.
func AssetInfo(name string) (os.FileInfo, error) {
	cannonicalName := strings.Replace(name, "\\", "/", -1)
	if f, ok := _bindata[cannonicalName]; ok {
		a, err := f()
		if err != nil {
			return nil, fmt.Errorf("AssetInfo %s can't read by error: %v", name, err)
		}
		return a.info, nil
	}
	return nil, fmt.Errorf("AssetInfo %s not found", name)
}

// AssetNames returns the names of the assets.
func AssetNames() []string {
	names := make([]string, 0, len(_bindata))
	for name := range _bindata {
		names = append(names, name)
	}
	return names
}

// _bindata is a table, holding each asset generator, mapped to its name.
var _bindata = map[string]func() (*asset, error){
	"dist/velox.min.js": distVeloxMinJs,
}

// AssetDir returns the file names below a certain
// directory embedded in the file by go-bindata.
// For example if you run go-bindata on data/... and data contains the
// following hierarchy:
//     data/
//       foo.txt
//       img/
//         a.png
//         b.png
// then AssetDir("data") would return []string{"foo.txt", "img"}
// AssetDir("data/img") would return []string{"a.png", "b.png"}
// AssetDir("foo.txt") and AssetDir("notexist") would return an error
// AssetDir("") will return []string{"data"}.
func AssetDir(name string) ([]string, error) {
	node := _bintree
	if len(name) != 0 {
		cannonicalName := strings.Replace(name, "\\", "/", -1)
		pathList := strings.Split(cannonicalName, "/")
		for _, p := range pathList {
			node = node.Children[p]
			if node == nil {
				return nil, fmt.Errorf("Asset %s not found", name)
			}
		}
	}
	if node.Func != nil {
		return nil, fmt.Errorf("Asset %s not found", name)
	}
	rv := make([]string, 0, len(node.Children))
	for childName := range node.Children {
		rv = append(rv, childName)
	}
	return rv, nil
}

type bintree struct {
	Func     func() (*asset, error)
	Children map[string]*bintree
}
var _bintree = &bintree{nil, map[string]*bintree{
	"dist": &bintree{nil, map[string]*bintree{
		"velox.min.js": &bintree{distVeloxMinJs, map[string]*bintree{}},
	}},
}}

// RestoreAsset restores an asset under the given directory
func RestoreAsset(dir, name string) error {
	data, err := Asset(name)
	if err != nil {
		return err
	}
	info, err := AssetInfo(name)
	if err != nil {
		return err
	}
	err = os.MkdirAll(_filePath(dir, filepath.Dir(name)), os.FileMode(0755))
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(_filePath(dir, name), data, info.Mode())
	if err != nil {
		return err
	}
	err = os.Chtimes(_filePath(dir, name), info.ModTime(), info.ModTime())
	if err != nil {
		return err
	}
	return nil
}

// RestoreAssets restores an asset under the given directory recursively
func RestoreAssets(dir, name string) error {
	children, err := AssetDir(name)
	// File
	if err != nil {
		return RestoreAsset(dir, name)
	}
	// Dir
	for _, child := range children {
		err = RestoreAssets(dir, filepath.Join(name, child))
		if err != nil {
			return err
		}
	}
	return nil
}

func _filePath(dir, name string) string {
	cannonicalName := strings.Replace(name, "\\", "/", -1)
	return filepath.Join(append([]string{dir}, strings.Split(cannonicalName, "/")...)...)
}

