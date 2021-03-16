// +build fio

/*
 * MinIO Cloud Storage, (C) 2020 MinIO, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	humanize "github.com/dustin/go-humanize"
	"github.com/minio/minio/pkg/disk"
	"github.com/minio/minio/pkg/ellipses"
	"github.com/minio/minio/pkg/env"
	xioutil "github.com/minio/minio/pkg/ioutil"
	"gonum.org/v1/gonum/stat"
)

const readBlockSize = 4 * humanize.MiByte // Default read block size 4MiB.

var pool = sync.Pool{
	New: func() interface{} {
		b := disk.AlignedBlock(readBlockSize)
		return &b
	},
}

const asciiLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890()"

var asciiLetterBytes [len(asciiLetters)]byte

func init() {
	for i, v := range asciiLetters {
		asciiLetterBytes[i] = byte(v)
	}
}

// randASCIIBytes fill destination with pseudorandom ASCII characters [a-ZA-Z0-9].
// Should never be considered for true random data generation.
func randASCIIBytes(dst []byte) {
	// Use a single seed.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	v := rng.Uint64()
	rnd := uint32(v)
	rnd2 := uint32(v >> 32)
	for i := range dst {
		dst[i] = asciiLetterBytes[int(rnd>>16)%len(asciiLetterBytes)]
		rnd ^= rnd2
		rnd *= 2654435761
	}
}

// Fallocate uses the linux Fallocate syscall, which helps us to be
// sure that subsequent writes on a file just created will not fail,
// in addition, file allocation will be contigous on the disk
func Fallocate(fd int, offset int64, len int64) error {
	// No need to attempt fallocate for 0 length.
	if len == 0 {
		return nil
	}
	// Don't extend size of file even if offset + len is
	// greater than file size from <bits/fcntl-linux.h>.
	fallocFLKeepSize := uint32(1)
	return syscall.Fallocate(fd, fallocFLKeepSize, offset, len)
}

type nullReader struct{}

func (r *nullReader) Read(b []byte) (int, error) {
	return len(b), nil
}

var debug = env.Get("DEBUG", "off") == "on"

// CreateFile - creates the file.
func write(obj int, drives []string, fileSize int64, tree bool) (time.Duration, error) {
	var nBuf [32]byte
	randASCIIBytes(nBuf[:])

	rv := rand.New(rand.NewSource(time.Now().UnixNano())).Intn
	var name string
	if tree {
		name = filepath.Join(drives[rv(len(drives))], fmt.Sprintf("%d/%s", obj, string(nBuf[:])))
	} else {
		name = filepath.Join(drives[rv(len(drives))], fmt.Sprintf("%d.%s", obj, string(nBuf[:])))
	}

	t := time.Now()

	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return 0, err
	}

	w, err := disk.OpenFileDirectIO(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0666)
	if err != nil {
		return 0, err
	}

	if fileSize > 0 {
		// Allocate needed disk space to append data
		err = Fallocate(int(w.Fd()), 0, fileSize)
	}
	if err != nil {
		return 0, err
	}

	defer func() {
		disk.Fdatasync(w) // Only interested in flushing the size_t not mtime/atime
		w.Close()
	}()

	bufp := pool.Get().(*[]byte)
	defer pool.Put(bufp)

	written, err := xioutil.CopyAligned(w, io.LimitReader(&nullReader{}, fileSize), *bufp, fileSize)
	if err != nil {
		return 0, err
	}

	if written != fileSize {
		return 0, fmt.Errorf("unexpected file size written expected %d, got %d", fileSize, written)
	}

	d := time.Since(t)
	if d > time.Second && debug {
		fmt.Printf("object %s took more than a second to write\n", name)
	}

	return d, nil
}

func concurrentWrite(obj int, drives []string, fileSize int64, nfiles int, totalIntervals []float64, tree bool) {
	var wg sync.WaitGroup
	wg.Add(int(nfiles))
	for i := 0; i < int(nfiles); i++ {
		i := i
		go func(i int) {
			defer wg.Done()
			d, err := write(obj+i, drives, fileSize, tree)
			if err != nil {
				log.Fatal(err)
			}
			totalIntervals[i] = float64(d)
		}(i)
	}
	wg.Wait()
}

// parseDrives will parse the drive parameter given.
func parseDrives(h string) []string {
	drives := strings.Split(h, ",")
	dst := make([]string, 0, len(drives))
	for _, drive := range drives {
		if !ellipses.HasEllipses(drive) {
			dst = append(dst, drive)
			continue
		}
		patterns, err := ellipses.FindEllipsesPatterns(drive)
		if err != nil {
			log.Fatal(err)
		}
		for _, p := range patterns {
			dst = append(dst, p.Expand()...)
		}
	}
	return dst
}

func main() {
	drives := parseDrives(env.Get("DRIVES", ""))
	if len(drives) == 0 {
		log.Fatal("DRIVES is a mandatory env option")
	}
	concurrency, err := strconv.Atoi(env.Get("CONCURRENT", "100"))
	if err != nil {
		log.Fatal(err)
	}
	fileSize, err := humanize.ParseBytes(env.Get("FILESIZE", "128KiB"))
	if err != nil {
		log.Fatal(err)
	}

	nfiles, err := humanize.ParseBytes(env.Get("NFILES", "8M"))
	if err != nil {
		log.Fatal(err)
	}

	tree, err := strconv.ParseBool(env.Get("TREE", "off"))
	if err != nil {
		log.Fatal(err)
	}

	var totalIntervals = make([]float64, nfiles)

	if int(nfiles) < concurrency {
		concurrentWrite(0, drives, int64(fileSize), int(nfiles), totalIntervals[:int(nfiles)], tree)
	} else {
		var i int
		for i < int(nfiles) {
			concurrentWrite(i, drives, int64(fileSize), concurrency, totalIntervals[i:i+concurrency], tree)
			i = i + concurrency
		}
	}
	sort.Float64s(totalIntervals)
	meanInterval, stdInterval := stat.MeanStdDev(totalIntervals, nil)
	fmt.Println("Mean time taken", time.Duration(meanInterval))
	fmt.Println("Standard deviation time taken", time.Duration(stdInterval))
	fmt.Println("Fastest time taken", time.Duration(totalIntervals[0]))
	fmt.Println("Slowest time taken", time.Duration(totalIntervals[len(totalIntervals)-1]))
}
