package modules

import (
	"reflect"
	"testing"
)

func TestParseCheckTimes(t *testing.T) {
	got, err := ParseCheckTimes([]string{"18:30", "6:00,06:00", " 12:05 "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"06:00", "12:05", "18:30"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"24:00", "6:5", "abc", "12:60", "1200", "-1:00"} {
		if _, err := ParseCheckTimes([]string{bad}); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	empty, err := ParseCheckTimes(nil)
	if err != nil || len(empty) != 0 || empty == nil {
		t.Errorf("empty input: got %v, %v", empty, err)
	}
}
