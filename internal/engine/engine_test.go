package engine

import "testing"

func TestIsMutating(t *testing.T) {
	cases := map[string]bool{
		"SELECT 1":                         false,
		"select * from users":              false,
		"-- drop table x\nSELECT 1":        false,
		"INSERT INTO t VALUES (1)":         true,
		"UPDATE t SET a=1":                 true,
		"DELETE FROM t":                    true,
		"DROP TABLE t":                     true,
		"CREATE TABLE t(id int)":           true,
		"WITH x AS (SELECT 1) SELECT * FROM x": false,
	}
	for sql, want := range cases {
		if got := IsMutating(sql); got != want {
			t.Fatalf("%q mutating=%v want %v", sql, got, want)
		}
	}
}
