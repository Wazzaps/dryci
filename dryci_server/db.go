package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"log"
	"net/http"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

func OpenDbPool() (*sqlitex.Pool, error) {
	return sqlitex.NewPool(*dbPath, sqlitex.PoolOptions{
		Flags:    sqlite.OpenReadWrite | sqlite.OpenCreate | sqlite.OpenWAL,
		PoolSize: 128,
		PrepareConn: func(conn *sqlite.Conn) error {
			// err := sqlitex.ExecuteTransient(conn, "PRAGMA synchronous = OFF", nil)
			// Consider "PRAGMA wal_autocheckpoint = 0;" for litestream
			err := sqlitex.ExecuteTransient(conn, "PRAGMA busy_timeout = 60000", nil)
			if err != nil {
				return fmt.Errorf("failed to prepare connection: %w", err)
			}
			return nil
		},
	})
}

func MigrateDb(dbPool *sqlitex.Pool, downgradeVersion int) (err error) {
	db, err := dbPool.Take(context.TODO())
	if err != nil {
		return fmt.Errorf("failed to take connection from pool: %w", err)
	}
	defer dbPool.Put(db)

	// Begin a write transaction
	endTxn, err := sqlitex.ImmediateTransaction(db)
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer endTxn(&err)

	// Initialize the settings table
	err = sqlitex.ExecuteTransient(db, `CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY NOT NULL,
		value TEXT
	)`, nil)
	if err != nil {
		return fmt.Errorf("failed to create settings table: %w", err)
	}

	// Get the current schema version
	schemaVersion := 0
	err = sqlitex.ExecuteTransient(db, "INSERT OR IGNORE INTO settings(key, value) VALUES('schema_version', 0)", nil)
	if err != nil {
		return fmt.Errorf("failed to set initial schema version: %w", err)
	}
	err = sqlitex.ExecuteTransient(db, "SELECT value FROM settings WHERE key = 'schema_version'", &sqlitex.ExecOptions{
		ResultFunc: func(stmt *sqlite.Stmt) error {
			schemaVersion = stmt.ColumnInt(0)
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("failed to get schema version: %w", err)
	}

	log.Println("Current DB schema version:", schemaVersion)

	// Downgrade to the requested version
	if downgradeVersion != -1 {
		for i := schemaVersion; i > downgradeVersion; i-- {
			sql, err := migrations.ReadFile(fmt.Sprintf("migrations/%d.down.sql", i))
			if err != nil {
				return fmt.Errorf("failed to find downgrade migration %d: %w", i, err)
			}
			log.Printf("- Unapplying migration %d", i)
			err = sqlitex.ExecuteScript(db, string(sql), nil)
			if err != nil {
				return fmt.Errorf("failed to apply downgrade migration %d: %w", i, err)
			}
			// Update the schema version
			err = sqlitex.Execute(db, "UPDATE settings SET value = ? WHERE key = 'schema_version'", &sqlitex.ExecOptions{
				Args: []interface{}{i - 1},
			})
			if err != nil {
				return fmt.Errorf("failed to decrement migration version %d: %w", i, err)
			}
		}
		schemaVersion = downgradeVersion
	}

	// Apply needed migrations in order
	for i := schemaVersion + 1; ; i++ {
		sql, err := migrations.ReadFile(fmt.Sprintf("migrations/%d.up.sql", i))
		if err != nil {
			break
		}
		log.Printf("- Applying migration %d", i)
		err = sqlitex.ExecuteScript(db, string(sql), nil)
		if err != nil {
			return fmt.Errorf("failed to apply migration %d: %w", i, err)
		}
		// Update the schema version
		err = sqlitex.Execute(db, "UPDATE settings SET value = ? WHERE key = 'schema_version'", &sqlitex.ExecOptions{
			Args: []interface{}{i},
		})
		if err != nil {
			return fmt.Errorf("failed to increment migration version %d: %w", i, err)
		}
	}

	// If we just upgraded from schema version 0, generate an admin token
	if schemaVersion == 0 {
		admin_uid := -1
		err = sqlitex.ExecuteTransient(db, "SELECT id FROM users WHERE superuser = 1", &sqlitex.ExecOptions{
			ResultFunc: func(stmt *sqlite.Stmt) error {
				if admin_uid != -1 {
					return fmt.Errorf("internal error: multiple admin users created after initial migration")
				}
				admin_uid = stmt.ColumnInt(0)
				return nil
			},
		})
		if err != nil {
			return fmt.Errorf("failed to get admin user ID: %w", err)
		}
		if admin_uid == -1 {
			return fmt.Errorf("internal error: no admin user found after initial migration")
		}
		token, err := CreateUserToken(db, admin_uid, 0)
		if err != nil {
			return fmt.Errorf("failed to generate admin token: %w", err)
		}
		log.Printf("Initial admin token: %s", token)
	}

	return err
}

func DbTxn(db *sqlite.Conn, writesToDb bool, f func() error) (err error) {
	if writesToDb {
		endTxn, err := sqlitex.ImmediateTransaction(db)
		if err != nil {
			return fmt.Errorf("failed to start transaction: %w", err)
		}
		defer endTxn(&err)
	} else {
		endTxn := sqlitex.Transaction(db)
		defer endTxn(&err)
	}
	return f()
}

var base32Enc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func GenToken() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		log.Fatalf("failed to generate random bytes: %w", err)
	}
	return "dryci-" + base32Enc.EncodeToString(b)
}

func CreateUserToken(db *sqlite.Conn, user_id int, expiration int) (string, error) {
	token := GenToken()
	if expiration == 0 {
		err := sqlitex.Execute(
			db,
			"INSERT INTO api_tokens(user_id, token) VALUES(?, ?)",
			&sqlitex.ExecOptions{Args: []interface{}{user_id, token}},
		)
		if err != nil {
			return "", fmt.Errorf("failed to insert user token: %w", err)
		}
	} else {
		currentTime := time.Now().Unix()
		expirationTime := currentTime + int64(expiration)
		err := sqlitex.Execute(
			db,
			"INSERT INTO api_tokens(user_id, token, expires_at) VALUES(?, ?, ?)",
			&sqlitex.ExecOptions{Args: []interface{}{user_id, token, expirationTime}},
		)
		if err != nil {
			return "", fmt.Errorf("failed to insert user token: %w", err)
		}
	}
	return token, nil
}

type Usage int

const (
	USAGE_QUERY   Usage = 1
	USAGE_PUBLISH Usage = 2
)

func AuthUser(db *sqlite.Conn, token string) (userId int, err error) {
	found := false

	// Early exit if the token is obviously invalid
	if len(token) > 40 || len(token) == 0 {
		return -1, HttpErrWrap(http.StatusUnauthorized, "Invalid Token", fmt.Errorf("token length out of bounds (%d)", len(token)))
	}

	err = sqlitex.Execute(
		db,
		`SELECT t.user_id, t.expires_at, t.disabled_at IS NULL AND u.disabled_at IS NULL
		FROM api_tokens t
		JOIN users u ON t.user_id = u.id
		WHERE token = ?`,
		&sqlitex.ExecOptions{
			Args: []interface{}{token},
			ResultFunc: func(stmt *sqlite.Stmt) error {
				found = true
				userId = stmt.ColumnInt(0)
				if !stmt.ColumnIsNull(1) {
					expiresAt := stmt.ColumnInt64(1)
					if time.Now().Unix() > expiresAt {
						return HttpErrWrap(
							http.StatusUnauthorized,
							"Token Expired",
							fmt.Errorf("token expired at %d for user %d", expiresAt, userId),
						)
					}
				}
				tokenEnabled := stmt.ColumnBool(2)
				if !tokenEnabled {
					return HttpErrWrap(
						http.StatusUnauthorized,
						"Token Disabled",
						fmt.Errorf("token disabled for user %d", userId),
					)
				}

				return nil
			},
		},
	)
	if err != nil {
		return -1, err
	}
	if !found {
		return -1, HttpErrWrap(http.StatusUnauthorized, "Invalid Token", fmt.Errorf("token not found"))
	}

	return
}

func RecordUsage(db *sqlite.Conn, userId int, usage Usage, timestamp time.Time) error {
	err := sqlitex.Execute(
		db,
		"INSERT INTO user_usage(timestamp, user_id, type, count) VALUES(?, ?, ?, ?)",
		&sqlitex.ExecOptions{Args: []interface{}{timestamp.Unix(), userId, usage, 1}},
	)
	if err != nil {
		return fmt.Errorf("failed to record usage: %w", err)
	}

	return nil
}

const DEP_HASH_HEX_SIZE = 64
const NODEID_HASH_HEX_SIZE = 32
const MAX_NODEIDS_PER_DEP = 32 * 1024

func QueryPassedTestHashes(db *sqlite.Conn, userId int, depHashes []string) ([][]string, error) {
	nodeIds := make([][]string, len(depHashes))
	for depHashIdx, depHash := range depHashes {
		if len(depHash) != DEP_HASH_HEX_SIZE {
			return nil, fmt.Errorf("invalid dep_hash length %d", len(depHash))
		}
		nodeIds[depHashIdx] = []string{}

		err := sqlitex.Execute(
			db,
			"SELECT node_ids FROM test_results WHERE user_id = ? AND dep_hash = ?",
			&sqlitex.ExecOptions{
				ResultFunc: func(stmt *sqlite.Stmt) error {
					concatedNodeIds := make([]byte, stmt.ColumnLen(0))
					stmt.ColumnBytes(0, concatedNodeIds)
					if len(concatedNodeIds)%NODEID_HASH_HEX_SIZE != 0 {
						return fmt.Errorf("invalid node_ids length %d of user:%d dep_hash:%s", len(nodeIds), userId, depHash)
					}

					for i := 0; i < len(concatedNodeIds); i += NODEID_HASH_HEX_SIZE {
						nodeIds[depHashIdx] = append(nodeIds[depHashIdx], string(concatedNodeIds[i:i+NODEID_HASH_HEX_SIZE]))
					}
					return nil
				},
				Args: []interface{}{userId, depHash},
			},
		)
		if err != nil {
			return nil, fmt.Errorf("failed to get node_ids of user:%d dep_hash:%s: %w", userId, depHash, err)
		}
	}

	return nodeIds, nil
}

func PublishTestHashes(db *sqlite.Conn, userId int, tests map[string][]string) error {
	for depHash, newNodeIds := range tests {
		if len(depHash) != DEP_HASH_HEX_SIZE {
			return fmt.Errorf("invalid dep_hash length %d", len(depHash))
		}
		if len(newNodeIds) > MAX_NODEIDS_PER_DEP {
			return fmt.Errorf("too many node_ids %d for dep_hash:%s", len(newNodeIds), depHash)
		}

		// Retrieve current nodeIds
		nodeIds := map[[NODEID_HASH_HEX_SIZE]byte]bool{}
		err := sqlitex.Execute(
			db,
			"SELECT node_ids FROM test_results WHERE user_id = ? AND dep_hash = ?",
			&sqlitex.ExecOptions{
				ResultFunc: func(stmt *sqlite.Stmt) error {
					if stmt.ColumnLen(0)%NODEID_HASH_HEX_SIZE != 0 {
						return fmt.Errorf("invalid node_ids length %d of user:%d dep_hash:%s", len(nodeIds), userId, depHash)
					}
					currentNodeIdCount := stmt.ColumnLen(0) / NODEID_HASH_HEX_SIZE
					if currentNodeIdCount+len(newNodeIds) > MAX_NODEIDS_PER_DEP {
						return fmt.Errorf("too many node_ids %d for dep_hash:%s", currentNodeIdCount+len(newNodeIds), depHash)
					}

					concatedNodeIds := make([]byte, stmt.ColumnLen(0))
					stmt.ColumnBytes(0, concatedNodeIds)

					for i := 0; i < len(concatedNodeIds); i += NODEID_HASH_HEX_SIZE {
						nodeIds[([NODEID_HASH_HEX_SIZE]byte)(concatedNodeIds[i:i+NODEID_HASH_HEX_SIZE])] = true
					}
					return nil
				},
				Args: []interface{}{userId, depHash},
			},
		)
		if err != nil {
			return fmt.Errorf("failed to get current node_ids: %w", err)
		}

		// Add new nodeIds
		for _, newNodeId := range newNodeIds {
			if len(newNodeId) != NODEID_HASH_HEX_SIZE {
				return fmt.Errorf("invalid node_id length %d", len(newNodeId))
			}
			nodeIds[([NODEID_HASH_HEX_SIZE]byte)([]byte(newNodeId))] = true
		}

		// Save nodeIds
		concatedNodeIds := make([]byte, 0, len(nodeIds)*NODEID_HASH_HEX_SIZE)
		for nodeId := range nodeIds {
			concatedNodeIds = append(concatedNodeIds, nodeId[:]...)
		}
		err = sqlitex.Execute(
			db,
			"INSERT OR REPLACE INTO test_results(user_id, dep_hash, accessed_at, node_ids) VALUES(?, ?, ?, ?)",
			&sqlitex.ExecOptions{
				Args: []interface{}{userId, depHash, time.Now().Unix(), concatedNodeIds},
			},
		)
		if err != nil {
			return fmt.Errorf("failed to save node_ids of user:%d dep_hash:%s: %w", userId, depHash, err)
		}
	}

	return nil
}
