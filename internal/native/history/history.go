// Package history stores provider attempts and exposes bounded, indexed queries.
package history

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/event"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/native/storage"
)

type Module struct {
	DB           *sql.DB
	AliasUpdater func(context.Context, string, string) error
}

func New(db *sql.DB) (*Module, error) {
	err := storage.Migrate(db, "history", `
	CREATE TABLE native_events (
	 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
	 timestamp_ms INTEGER NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
	 account TEXT NOT NULL, key_hash TEXT NOT NULL, request_id TEXT NOT NULL,
	 failed INTEGER NOT NULL, status_code INTEGER NOT NULL, latency_ms INTEGER NOT NULL,
	 input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, cache_read_tokens INTEGER NOT NULL,
	 cache_write_tokens INTEGER NOT NULL, reasoning_tokens INTEGER NOT NULL, total_tokens INTEGER NOT NULL,
	 cost_usd REAL, payload TEXT NOT NULL);
	CREATE INDEX native_events_time ON native_events(timestamp_ms);
	CREATE INDEX native_events_model_time ON native_events(model,timestamp_ms);
	CREATE INDEX native_events_account_time ON native_events(account,timestamp_ms);
	CREATE INDEX native_events_key_time ON native_events(key_hash,timestamp_ms);
	CREATE INDEX native_events_request ON native_events(request_id);
	CREATE TABLE native_aliases (key_hash TEXT PRIMARY KEY, label TEXT NOT NULL);
	`, `
 ALTER TABLE native_events ADD COLUMN trace_id TEXT NOT NULL DEFAULT '';
 ALTER TABLE native_events ADD COLUMN ttft_ms INTEGER;
 ALTER TABLE native_events ADD COLUMN latency_observed INTEGER NOT NULL DEFAULT 0;
 UPDATE native_events SET latency_observed=(latency_ms>0), ttft_ms=CASE WHEN json_extract(payload,'$.stream')=1 AND json_extract(payload,'$.ttft_ms')>0 THEN json_extract(payload,'$.ttft_ms') END;
 CREATE INDEX native_events_trace ON native_events(trace_id,seq);
 CREATE INDEX native_events_latency ON native_events(latency_ms DESC,seq DESC);
 `)
	return &Module{DB: db}, err
}

func Insert(tx *sql.Tx, e event.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO native_events
	(id,timestamp_ms,provider,model,account,key_hash,request_id,failed,status_code,latency_ms,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,total_tokens,cost_usd,payload,trace_id,ttft_ms,latency_observed)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		e.ID, e.TimestampMS, e.Provider, e.Model, e.Account, e.KeyHash, e.RequestID, e.Failed, e.StatusCode, e.LatencyMS,
		e.Tokens.Input.TotalTokens, e.Tokens.Output.TotalTokens, e.Tokens.Input.CacheReadTokens, e.Tokens.Input.CacheWriteTokens, e.Tokens.Output.ReasoningTokens, e.Tokens.TotalTokens, e.CostUSD, string(b), e.TraceID, e.TTFTObservedMS, e.LatencyObserved)
	return err
}
