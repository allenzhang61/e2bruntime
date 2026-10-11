package main

import (
	"reflect"
	"testing"
)

func TestRetentionKeepsNewestTen(t *testing.T) {
	keys := []string{"09", "01", "11", "07", "03", "02", "10", "08", "06", "05", "04"}
	got := expired(keys, 10)
	if !reflect.DeepEqual(got, []string{"01"}) {
		t.Fatal(got)
	}
	if len(expired(keys[:10], 10)) != 0 {
		t.Fatal("deleted below limit")
	}
}
