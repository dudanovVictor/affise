package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sync/atomic"
)

const (
	MaxUrlsPerRequest    = 20
	MaxRequests          = 50
	MaxUrlGetters        = 4
	MaxUrlGetTimeSeconds = 1
)

type UrlsReq []string

type Semaphore interface {
	Acquire()
	Release()
}
type semaphore struct {
	semChannel chan struct{}
}

func NewSemaphore(maxConcurrency int) Semaphore {
	return &semaphore{
		semChannel: make(chan struct{}, maxConcurrency),
	}
}
func (s *semaphore) Acquire() {
	s.semChannel <- struct{}{}
}
func (s *semaphore) Release() {
	<-s.semChannel
}

var reqCounter int64

func handler(w http.ResponseWriter, req *http.Request) {
	value := atomic.AddInt64(&reqCounter, 1)
	defer atomic.AddInt64(&reqCounter, -1)
	if value > MaxRequests {
		http.Error(w, fmt.Sprintf("%d>%d", reqCounter, MaxRequests), http.StatusTooManyRequests)
		return
	}
	ctx := req.Context()
	if req.Method == http.MethodPost {
		var u UrlsReq
		if err := json.NewDecoder(req.Body).Decode(&u); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(u) > MaxUrlsPerRequest {
			http.Error(w, fmt.Sprintf("too many urls: %d > %d", len(u), MaxUrlsPerRequest), http.StatusBadRequest)
			return
		}
		strs, err := processUrls(ctx, u)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(strs)
	} else {
		http.Error(w, fmt.Sprintf("method: %s", req.Method), http.StatusBadRequest)
		return
	}

}

func processUrls(ctx context.Context, arr UrlsReq) ([]string, error) {
	res := make([]string, len(arr))
	var err error
	sem := NewSemaphore(MaxUrlGetters)
	doneChannel := make(chan bool, 1)
	totalGetters := len(arr)
	var resultCounter int64

	for i := 0; i < totalGetters; i++ {
		sem.Acquire()
		select {
		case <-ctx.Done():
			err = ctx.Err()
			return res, fmt.Errorf("%s: %w", http.StatusInternalServerError, err)
		default:
		}
		go func(i int) {
			defer sem.Release()
			ctxGet, cancel := context.WithTimeout(ctx, time.Duration(time.Second*MaxUrlGetTimeSeconds))
			defer cancel()
			res[i], err = get(arr[i], ctxGet)
			value := atomic.AddInt64(&resultCounter, 1)
			if value == int64(totalGetters) || err != nil {
				doneChannel <- true
			}
		}(i)
	}
	<-doneChannel
	return res, err
}
func get(u string, ctx context.Context) (string, error) {
	if u == "" {
		return "", errors.New("empty url")
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("http new request for %s: %w", u, err)
	}
	req = req.WithContext(ctx)

	c := &http.Client{}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("http GET: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read resp body for %s: %w", u, err)
	}
	return string(body), nil
}

func main() {
	exitChannel := make(chan os.Signal)
	signal.Notify(exitChannel, syscall.SIGTERM, syscall.SIGINT)

	http.HandleFunc("/go", handler)
	server := &http.Server{Addr: "localhost:8080"}
	go func() {
		defer func() { exitChannel <- syscall.SIGTERM }()
		err := server.ListenAndServe()
		if err != nil {
			fmt.Println(err)
		}
	}()
	<-exitChannel
	ctx := context.Background()
	server.Shutdown(ctx)
}
