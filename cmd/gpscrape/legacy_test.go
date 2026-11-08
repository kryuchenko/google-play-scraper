package main

import (
	"encoding/json"
	"testing"
)

// A catalog directory from v1.6 or earlier has camelCase manifests, sweep
// state and new-app logs; they must still read.
func TestLegacyCatalogFilesStillRead(t *testing.T) {
	var m manifest
	if err := json.Unmarshal([]byte(`{"generation":{"date":"2026-08-23","run":"1787500934","shards":83445},"completedAt":"2026-08-23T10:00:00Z","ids":5,"failedShards":2,"file":"ids.gz","sha256":"x","samplePct":1.5,"sampleSeed":7}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.CompletedAt == "" || m.FailedShard != 2 || m.SamplePct != 1.5 || m.SampleSeed != 7 || m.Generation.Shards != 83445 {
		t.Errorf("legacy manifest: %+v", m)
	}
	var s syncState
	if err := json.Unmarshal([]byte(`{"generation":{"date":"d","run":"r","shards":1},"ids":3,"samplePct":0.5,"sampleSeed":9}`), &s); err != nil || s.SamplePct != 0.5 || s.SampleSeed != 9 {
		t.Errorf("legacy state: %+v %v", s, err)
	}
	var r newRecord
	if err := json.Unmarshal([]byte(`{"appId":"com.a","category":"GAME","rank":3,"seen":"first","at":"2026-01-01T00:00:00Z"}`), &r); err != nil || r.AppID != "com.a" || r.Rank != 3 {
		t.Errorf("legacy new record: %+v %v", r, err)
	}

	// New files are snake_case, and a snake_case key wins over a stale twin.
	b, _ := json.Marshal(m)
	var keys map[string]any
	_ = json.Unmarshal(b, &keys)
	for _, k := range []string{"completed_at", "failed_shards", "sample_pct", "sample_seed"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("written manifest lacks %s: %s", k, b)
		}
	}
	var both newRecord
	_ = json.Unmarshal([]byte(`{"appId":"old","app_id":"new"}`), &both)
	if both.AppID != "new" {
		t.Errorf("snake_case key should win, got %q", both.AppID)
	}
}
