package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
	_ "zombiezen.com/go/sqlite/sqlitex"
)

const VERSION = "1.0.0"

//go:generate ./gen_commit_info.sh
//go:embed commit_info.txt
var commit_info []byte

//go:embed migrations/*.sql
var migrations embed.FS

// -- Flags --

// TODO: make env vars instead?
var dbPath = flag.String("db", "dryci.db", "Path to the SQLite database file")
var listenAddr = flag.String("listen", "127.0.0.1:8080", "Address to listen on")
var showVersion = flag.Bool("version", false, "Show version information")
var dbDowngrade = flag.Int("db-downgrade", -1, "Downgrade the database schema to the specified version before applying migrations (destructive!)")

func getFullVersion() string {
	build_info_str := ""
	build_info, ok := debug.ReadBuildInfo()
	if ok {
		build_info_str = fmt.Sprintf("%v", build_info)
	}
	return fmt.Sprintf("version\t%s\n%s%s", VERSION, build_info_str, commit_info)
}

type UsageRecord struct {
	Timestamp time.Time
	UserId    int
	Usage     Usage
}

type ApiServer struct {
	dbPool        *sqlitex.Pool
	bgProcessChan chan interface{}
}

type QueryPassedRequest struct {
	TestFileHashes []string `json:"test_file_hashes"`
}

type QueryPassedResponse struct {
	NodeIds [][]string `json:"node_ids"`
}

func (s *ApiServer) QueryPassedHandler(db *sqlite.Conn, req *QueryPassedRequest, res *QueryPassedResponse, userId int) error {
	nodeIds, err := QueryPassedTestHashes(db, userId, req.TestFileHashes)
	if err != nil {
		return err
	}
	*res = QueryPassedResponse{NodeIds: nodeIds}
	return nil
}

type PublishRequest struct {
	PassedNodeIdsPerTestFile map[string][]string `json:"passed_node_ids_per_test_file"`
	TotalTestCount           int                 `json:"total_test_count"`
	PassedTestCount          int                 `json:"passed_test_count"`
	FailedTestCount          int                 `json:"failed_test_count"`
	SkippedTestCount         int                 `json:"skipped_test_count"`
	SkippedByCacheTestCount  int                 `json:"skipped_by_cache_test_count"`
}

type PublishResponse struct {
}

type UserPublishRequest struct {
	Req    *PublishRequest
	UserId int
}

func (s *ApiServer) PublishHandler(_ *sqlite.Conn, req *PublishRequest, res *PublishResponse, userId int) error {
	*res = PublishResponse{}
	s.bgProcessChan <- UserPublishRequest{Req: req, UserId: userId}
	return nil
}

func (s *ApiServer) ApiDocHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fullWrite(w, `Welcome to the DryCI API Documentation!

--- GET /api/ ---------------------------------------------------------------------------------------------------------
    Human-readable API documentation


--- POST /api/v1/query-passed -----------------------------------------------------------------------------------------
    Query successful node IDs for a list of test file hashes (dep-hashes).
    Returns a list of lists of node IDs, one list per test file hash.

    Example request:
        {
            "test_file_hashes": [
                "ed69bb4aa4547f7d83799875d800d4158a125c2316fe1bddb6a6a79ad8611b48",
                "65fe2ae6a67ebab19ca6c79b85d6feba73c87d1bc09c36bf1ebcf85d48dd13e6"
            ]
        }
    
    Example response:
        {
            "node_ids": [
                ["1b643e95eed492e780a485f9c40dc15b", "6a0f78ba19acfef983e2d11ef7b6f54c", "546afde6eb0c7304bdb39e4d839a4025"],
                ["12c2461ddf13d3a84755044f8fb93513", "3b8854ee811b571e97c73038e40b8b8b", "97b36957762247ce0a1077109bca3d22"]
            ]
        }


--- POST /api/v1/publish ----------------------------------------------------------------------------------------------
    Publish successful test node ids for a run. The node ids are grouped by the test file hash (dep-hash).

    Example request:
        {
            "passed_node_ids_per_test_file": {
                "ed69bb4aa4547f7d83799875d800d4158a125c2316fe1bddb6a6a79ad8611b48": [
                    "1b643e95eed492e780a485f9c40dc15b", "6a0f78ba19acfef983e2d11ef7b6f54c", "546afde6eb0c7304bdb39e4d839a4025"
                ],
                "65fe2ae6a67ebab19ca6c79b85d6feba73c87d1bc09c36bf1ebcf85d48dd13e6": [
                    "12c2461ddf13d3a84755044f8fb93513", "3b8854ee811b571e97c73038e40b8b8b", "97b36957762247ce0a1077109bca3d22"
                ]
            }
        }
    
    Example response:
        {}
`)
}

func (s *ApiServer) backgroundHandler(db *sqlite.Conn, items []interface{}) {
	start := time.Now()
	for _, item := range items {
		switch item := item.(type) {
		case UsageRecord:
			err := RecordUsage(db, item.UserId, item.Usage, item.Timestamp)
			if err != nil {
				log.Printf("Failed to record usage: %v", err)
				continue
			}
		case UserPublishRequest:
			err := PublishTestHashes(db, item.UserId, item.Req.PassedNodeIdsPerTestFile)
			if err != nil {
				log.Printf("Failed to publish test results: %v", err)
				continue
			}
		default:
			log.Printf("Unknown background task type: %T", item)
		}
	}
	log.Printf("Processed %d background tasks in %v", len(items), time.Since(start))
}

func main() {
	flag.Parse()

	if *showVersion {
		fmt.Println(getFullVersion())
		return
	}
	log.Printf("dryci_server v%s", VERSION)

	// Open the DB
	dbPool, err := OpenDbPool()
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer dbPool.Close()

	// Perform migrations
	err = MigrateDb(dbPool, *dbDowngrade)
	if err != nil {
		log.Fatalf("Failed to migrate database: %v", err)
	}

	// Start the server
	api_server := ApiServer{
		dbPool:        dbPool,
		bgProcessChan: make(chan interface{}, 16*1024),
	}
	http_server := http.Server{
		Addr:              *listenAddr,
		ReadTimeout:       2 * time.Second,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      2 * time.Second,
		IdleTimeout:       10 * time.Second,
	}
	http.HandleFunc("GET /", api_server.ApiDocHandler)
	http.HandleFunc("GET /api", api_server.ApiDocHandler)
	http.HandleFunc("POST /api/v1/query-passed", jsonApi(&api_server, false, USAGE_QUERY, api_server.QueryPassedHandler))
	http.HandleFunc("POST /api/v1/publish", jsonApi(&api_server, false, USAGE_PUBLISH, api_server.PublishHandler))

	// Start background goroutines
	done := make(chan struct{})
	bgDb, err := dbPool.Take(context.Background())
	if err != nil {
		log.Fatalf("Failed to take database connection for background committer: %v", err)
	}
	go batchedBackgroundWorker(
		api_server.bgProcessChan,
		done,
		bgDb,
		100*time.Millisecond,
		api_server.backgroundHandler,
	)

	log.Printf("Listening on %s", *listenAddr)
	http_server.ListenAndServe()
	close(done)
}
