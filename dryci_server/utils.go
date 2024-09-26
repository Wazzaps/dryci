package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"zombiezen.com/go/sqlite"
)

func fullWriteBytes(w io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := w.Write(data)
		if err != nil {
			return err
		}
		data = data[written:]
	}
	return nil
}

func fullWrite(w io.Writer, data string) error {
	return fullWriteBytes(w, []byte(data))
}

const MAX_REQUEST_BODY_BYTES = 8 * 1024 * 1024

func readRequest(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	lr := io.LimitedReader{R: r.Body, N: MAX_REQUEST_BODY_BYTES}
	dec := json.NewDecoder(&lr)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err != nil {
		if lr.N == 0 {
			http.Error(w, "Request Entity Too Large, see /api/ for documentation", http.StatusRequestEntityTooLarge)
			log.Printf("%d %s: Request Entity Too Large\n", http.StatusRequestEntityTooLarge, r.URL.Path)
			return false
		}
		http.Error(w, fmt.Sprintf("Unprocessable Entity, see /api/ for documentation: %v", err), http.StatusUnprocessableEntity)
		log.Printf("%d %s: Unprocessable Entity: %v\n", http.StatusUnprocessableEntity, r.URL.Path, err)
		return false
	}
	return true
}

func sendResponse(w http.ResponseWriter, r *http.Request, v interface{}, handlerErr error, handleStart time.Time) {
	enc := json.NewEncoder(w)

	var httpStatus int
	var httpMessage string
	if hc, ok := handlerErr.(HttpCode); ok {
		httpStatus = hc.Code()
		httpMessage = hc.Message()
	} else if handlerErr != nil {
		httpStatus = http.StatusInternalServerError
		httpMessage = "Internal Server Error"
	} else {
		httpStatus = http.StatusOK
		httpMessage = "OK"
	}

	handleTime := time.Since(handleStart)
	if handlerErr != nil {
		http.Error(w, httpMessage, httpStatus)
		log.Printf("%s %d %s: %v\n", handleTime, httpStatus, r.URL.Path, handlerErr)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	err := enc.Encode(v)
	if err != nil {
		log.Printf("%s %d %s: %v\n", handleTime, httpStatus, r.URL.Path, err)
	} else {
		log.Printf("%s %d %s\n", handleTime, httpStatus, r.URL.Path)
	}
}

func jsonApi[INP interface{}, OUT interface{}](
	s *ApiServer,
	writesToDb bool,
	usage Usage,
	handler func(db *sqlite.Conn, req *INP, res *OUT, userId int) error,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		authorization := r.Header.Get("Authorization")
		token, hasToken := strings.CutPrefix(authorization, "Bearer ")
		if !hasToken {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		db, err := s.dbPool.Take(r.Context())
		if err != nil {
			sendResponse(w, r, nil, HttpErrWrap(http.StatusServiceUnavailable, "Server overloaded, try again later", err), start)
			return
		}
		defer s.dbPool.Put(db)

		userId := -1
		err = DbTxn(db, false, func() error {
			uid, err := AuthUser(db, token)
			userId = uid
			return err
		})
		if err != nil {
			sendResponse(w, r, nil, err, start)
			return
		}

		var req INP
		success := readRequest(w, r, &req)
		if !success {
			return
		}

		s.bgProcessChan <- UsageRecord{
			Timestamp: time.Now(),
			UserId:    userId,
			Usage:     usage,
		}

		var res OUT
		err = DbTxn(db, writesToDb, func() error {
			return handler(db, &req, &res, userId)
		})

		sendResponse(w, r, res, err, start)
	}
}

type HttpErrWrapper struct {
	code    int
	message string
	err     error
}

func (h HttpErrWrapper) Code() int {
	return h.code
}

func (h HttpErrWrapper) Message() string {
	return h.message
}

func (h HttpErrWrapper) Error() string {
	if h.err == nil {
		return h.message
	}
	return h.err.Error()
}

func (h HttpErrWrapper) Context(format string, args ...interface{}) HttpErrWrapper {
	args = append(args, h.err)
	h.err = fmt.Errorf(format+": %w", args...)
	return h
}

func HttpErrWrap(code int, message string, err error) HttpErrWrapper {
	return HttpErrWrapper{code: code, message: message, err: err}
}

type HttpCode interface {
	Code() int
	Message() string
}

func batchedBackgroundWorker(
	work <-chan interface{},
	done <-chan struct{},
	db *sqlite.Conn,
	batchTime time.Duration,
	handler func(db *sqlite.Conn, items []interface{}),
) {

	isDone := false
	isSleeping := true
	workItems := []interface{}{}
	nextCommitTime := time.Now().Add(9999 * time.Hour)
	for !isDone {
		doCommit := false
		select {
		case <-done:
			isDone = true
			doCommit = true
		case <-time.After(time.Until(nextCommitTime)):
			nextCommitTime = time.Now().Add(9999 * time.Hour)
			isSleeping = true
			doCommit = true
		case item := <-work:
			workItems = append(workItems, item)
			if isSleeping {
				nextCommitTime = time.Now().Add(batchTime)
				isSleeping = false
			}
		}

		if doCommit && len(workItems) > 0 {
			err := DbTxn(db, true, func() error {
				// TODO: Consider subtransactions (savepoints) for isolation
				handler(db, workItems)
				return nil
			})
			workItems = []interface{}{}
			if err != nil {
				log.Printf("Failed to commit worker changes: %v", err)
			}
		}
	}
}
