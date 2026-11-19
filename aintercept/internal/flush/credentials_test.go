package flush

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Blink-Build-Studios/aintercept/aintercept/internal/records"
)

func credential(keyHash, acct, source string, ns int64) records.Record {
	return records.Record{Kind: records.KindCredential, Credential: &records.Credential{
		KeyHash: keyHash, KeyHint: "sk-ant…" + keyHash[len(keyHash)-1:], AccountID: acct, Source: source, NS: ns,
	}}
}

func credentialRows(t *testing.T, rs ...records.Record) []string {
	t.Helper()
	db := fresh(t)
	write(t, db, rs...)
	rows, err := db.Query(ctx(t), `SELECT key_hash, key_hint, account_id, source, first_ns, verified_ns FROM credentials ORDER BY key_hash`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var kh, hint, acct, src string
		var first, verified int64
		if err := rows.Scan(&kh, &hint, &acct, &src, &first, &verified); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %s %s %s %d %d", kh, hint, acct, src, (first-t0)/minute, (verified-t0)/minute))
	}
	return got
}

// The newest sighting's account, source and hint; the earliest and latest
// times; many credentials to one account, in one flush or over several.
func TestCredentialsUpsert(t *testing.T) {
	recs := []records.Record{
		credential("kh-1", "acct1", "lookup", t0+2*minute),
		credential("kh-1", "acct1", "observed", t0+5*minute),
		credential("kh-1", "acct-old", "lookup", t0),
		credential("kh-2", "acct1", "observed", t0+minute),
		credential("kh-3", "acct2", "lookup", t0+3*minute),
	}
	want := []string{"kh-1 sk-ant…1 acct1 observed 0 5", "kh-2 sk-ant…2 acct1 observed 1 1", "kh-3 sk-ant…3 acct2 lookup 3 3"}
	if got := credentialRows(t, recs...); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("one flush:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	db := fresh(t)
	for i := len(recs) - 1; i >= 0; i-- { // one record a flush, newest first
		write(t, db, recs[i])
	}
	var n int
	if err := db.QueryRow(ctx(t), `SELECT count(*) FROM credentials WHERE account_id = 'acct1'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("acct1 has %d credentials (%v), want 2", n, err)
	}
	var acct, src string
	var first, verified int64
	if err := db.QueryRow(ctx(t), `SELECT account_id, source, first_ns, verified_ns FROM credentials WHERE key_hash = 'kh-1'`).Scan(&acct, &src, &first, &verified); err != nil ||
		acct != "acct1" || src != "observed" || first != t0 || verified != t0+5*minute {
		t.Errorf("flushed one by one: %s %s %d %d %v", acct, src, first, verified, err)
	}
}
