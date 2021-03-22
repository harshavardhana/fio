// +build nio

/*
 * MinIO Cloud Storage, (C) 2021 MinIO, Inc.
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
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
	xhttp "github.com/minio/minio/cmd/http"
	"github.com/minio/minio/cmd/logger"
	"github.com/minio/minio/cmd/rest"
	"github.com/minio/minio/pkg/env"
	"gonum.org/v1/gonum/stat"
)

var (
	client    bool
	defaultTR bool
	url       string
)

var globalDNSCache = xhttp.NewDNSCache(10*time.Second, 10*time.Second, logger.LogOnceIf)

func init() {
	flag.BoolVar(&client, "client", false, "indicates if its a client")
	flag.BoolVar(&defaultTR, "defaultTR", false, "indicates if Go default transport to use")
	flag.StringVar(&url, "url", "http://localhost:9090", "url to the server")
}

func newInternodeDefaultTransport(tlsConfig *tls.Config, dialTimeout time.Duration) http.RoundTripper {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:       dialTimeout,
			KeepAlive:     dialTimeout,
			FallbackDelay: 100 * time.Millisecond,
		}).DialContext,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 3 * time.Minute, // Set conservative timeouts for MinIO internode.
		TLSHandshakeTimeout:   90 * time.Second,
		ExpectContinueTimeout: 90 * time.Second,
		TLSClientConfig:       tlsConfig,
		// Set this value so that the underlying transport round-tripper
		// doesn't try to auto decode the body of objects with
		// content-encoding set to `gzip`.
		//
		// Refer:
		//    https://golang.org/src/net/http/transport.go?h=roundTrip#L1843
		DisableCompression: true,
	}

	return tr
}

func newInternodeHTTPTransport(tlsConfig *tls.Config, dialTimeout time.Duration) http.RoundTripper {
	// For more details about various values used here refer
	// https://golang.org/pkg/net/http/#Transport documentation
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           xhttp.DialContextWithDNSCache(globalDNSCache, xhttp.NewInternodeDialContext(dialTimeout)),
		MaxIdleConnsPerHost:   64,
		WriteBufferSize:       32 << 10, // 32KiB moving up from 4KiB default
		ReadBufferSize:        32 << 10, // 32KiB moving up from 4KiB default
		IdleConnTimeout:       15 * time.Second,
		ResponseHeaderTimeout: 3 * time.Minute, // Set conservative timeouts for MinIO internode.
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 15 * time.Second,
		TLSClientConfig:       tlsConfig,
		// Go net/http automatically unzip if content-type is
		// gzip disable this feature, as we are always interested
		// in raw stream.
		DisableCompression: true,
	}

	return tr
}

func main() {
	flag.Parse()

	tr := newInternodeHTTPTransport(nil, rest.DefaultTimeout)
	clnt := http.Client{Transport: tr}
	if defaultTR {
		clnt.Transport = newInternodeDefaultTransport(nil, rest.DefaultTimeout)
	}
	concurrency, err := strconv.Atoi(env.Get("CONCURRENT", "100"))
	if err != nil {
		log.Fatal(err)
	}
	if client {
		for {
			var totalIntervals = make([]float64, concurrency)
			var wg sync.WaitGroup
			wg.Add(concurrency)
			for i := 0; i < concurrency; i++ {
				i := i
				go func() {
					defer wg.Done()
					req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
					if err != nil {
						log.Fatal(err)
					}
					t := time.Now()
					resp, err := clnt.Do(req)
					if err != nil {
						log.Fatal(err)
					}
					if resp.StatusCode != http.StatusOK {
						log.Fatal("server returned unexpected response code")
					}
					totalIntervals[i] = float64(time.Since(t))
					io.Copy(ioutil.Discard, resp.Body)
					resp.Body.Close()
				}()
			}
			wg.Wait()
			sort.Float64s(totalIntervals)
			meanInterval, stdInterval := stat.MeanStdDev(totalIntervals, nil)
			fmt.Println("Mean time taken", time.Duration(meanInterval))
			fmt.Println("Standard deviation time taken", time.Duration(stdInterval))
			fmt.Println("Fastest time taken", time.Duration(totalIntervals[0]))
			fmt.Println("Slowest time taken", time.Duration(totalIntervals[len(totalIntervals)-1]))
			time.Sleep(3 * time.Second)
			fmt.Println("Continuing the next set of runs")
		}
	}

	router := mux.NewRouter().SkipClean(true).UseEncodedPath()
	router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		return
	})

	if defaultTR {
		s := &http.Server{
			Addr:           ":9090",
			Handler:        router,
			MaxHeaderBytes: 1 << 20,
		}
		log.Fatal(s.ListenAndServe())
	} else {
		httpServer := xhttp.NewServer([]string{":9090"}, router, nil)
		httpServer.Start()
	}
}
